package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// DeviceHandler serves DayMug-Client: pairing, the client's outbound tool
// socket, the MCP endpoint bound conversations' agents call, and the
// conversation ↔ device binding. A plain browser never calls any of it; the
// page only does once it finds the client's injected marker.
type DeviceHandler struct {
	Store   store.Store
	Hub     *service.DeviceHub
	Version string
	// Busy reports whether a conversation has a turn in flight
	// (Broadcaster.IsBusy); nil reports none.
	Busy func(conversationID string) bool
}

const (
	deviceHelloTimeout = 10 * time.Second
	deviceWriteTimeout = 30 * time.Second
	deviceReadLimit    = 64 << 20
	devicePingInterval = 25 * time.Second
	deviceMCPBodyLimit = 32 << 20
)

type deviceView struct {
	store.Device
	Online   bool   `json:"online"`
	Hostname string `json:"hostname,omitempty"`
	Version  string `json:"version,omitempty"`
	Home     string `json:"home,omitempty"`
}

func (h *DeviceHandler) view(d store.Device) deviceView {
	v := deviceView{Device: d}
	if hello, ok := h.Hub.Hello(d.ID); ok {
		v.Online = true
		v.Hostname = hello.Hostname
		v.Version = hello.Version
		v.Home = hello.Home
	}
	return v
}

// List returns the caller's paired devices.
func (h *DeviceHandler) List(c *gin.Context) {
	devices, err := h.Store.ListDevices(c.Request.Context(), middleware.CurrentUserID(c))
	if err != nil {
		respondInternalError(c, "devices", err)
		return
	}
	out := make([]deviceView, 0, len(devices))
	for _, d := range devices {
		out = append(out, h.view(d))
	}
	c.JSON(http.StatusOK, gin.H{"devices": out})
}

// Revoke deletes one of the caller's devices and drops its connection.
func (h *DeviceHandler) Revoke(c *gin.Context) {
	err := h.Hub.Revoke(c.Request.Context(), middleware.CurrentUserID(c), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
		return
	}
	if err != nil {
		respondInternalError(c, "devices", err)
		return
	}
	c.Status(http.StatusNoContent)
}

type pairRequest struct {
	Nonce string `json:"nonce"`
	Name  string `json:"name"`
	OS    string `json:"os"`
}

// Pair is called by the page hosted in DayMug-Client; see DeviceHub.BeginPair.
func (h *DeviceHandler) Pair(c *gin.Context) {
	var req pairRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if err := h.Hub.BeginPair(c.Request.Context(), middleware.CurrentUserID(c), req.Nonce, req.Name, req.OS); err != nil {
		respondServiceError(c, err)
		return
	}
	c.Status(http.StatusAccepted)
}

// Current reports which of the caller's devices hosts this page.
func (h *DeviceHandler) Current(c *gin.Context) {
	d, _, ok := h.Hub.CurrentDevice(middleware.CurrentUserID(c), c.Query("nonce"))
	if !ok {
		c.JSON(http.StatusOK, gin.H{"device": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"device": h.view(d)})
}

// Claim is the client's unauthenticated poll for the credentials its page
// requested. 204 means "nothing yet".
func (h *DeviceHandler) Claim(c *gin.Context) {
	var req struct {
		Nonce string `json:"nonce"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	device, token, ok, err := h.Hub.Claim(c.Request.Context(), req.Nonce)
	if err != nil {
		respondInternalError(c, "devices", err)
		return
	}
	if !ok {
		c.Status(http.StatusNoContent)
		return
	}
	c.JSON(http.StatusOK, gin.H{"device_id": device.ID, "token": token})
}

func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

// Connect is the client's long-lived socket. The client dials out, so it works
// from behind NAT; the server pushes tool calls down it.
func (h *DeviceHandler) Connect(c *gin.Context) {
	device, err := h.Hub.Authenticate(c.Request.Context(), bearerToken(c.Request))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid device token"})
		return
	}
	conn, ctx, cancel, ok := acceptWS(c.Writer, c.Request, "device ws accept")
	if !ok {
		return
	}
	defer cancel()
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(deviceReadLimit)

	helloCtx, helloCancel := context.WithTimeout(ctx, deviceHelloTimeout)
	_, data, err := conn.Read(helloCtx)
	helloCancel()
	if err != nil {
		return
	}
	var first service.DeviceFrame
	if json.Unmarshal(data, &first) != nil || first.Type != "hello" || first.Hello == nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "expected hello")
		return
	}
	send := func(frame service.DeviceFrame) error {
		payload, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		wctx, wcancel := context.WithTimeout(ctx, deviceWriteTimeout)
		defer wcancel()
		return conn.Write(wctx, websocket.MessageText, payload)
	}
	session := h.Hub.Register(device, *first.Hello, send)
	defer session.Close()
	log.Printf("[device] %s (%s) connected: %s %s", device.ID, device.Name, first.Hello.OS, first.Hello.Version)
	defer log.Printf("[device] %s disconnected", device.ID)

	go func() {
		select {
		case <-session.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	startWSPing(ctx, cancel, conn, devicePingInterval)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var frame service.DeviceFrame
		if json.Unmarshal(data, &frame) != nil {
			continue
		}
		session.Deliver(frame)
	}
}

type deviceActivityConversation struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Activity tells a client which of the conversations bound to it have a turn
// running right now, so closing its window can warn that those turns would
// lose their tools. Authenticated with the device token, like Connect.
func (h *DeviceHandler) Activity(c *gin.Context) {
	device, err := h.Hub.Authenticate(c.Request.Context(), bearerToken(c.Request))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid device token"})
		return
	}
	ids, err := h.Store.ListDeviceConversations(c.Request.Context(), device.ID)
	if err != nil {
		respondInternalError(c, "devices", err)
		return
	}
	running := []deviceActivityConversation{}
	for _, id := range ids {
		if h.Busy == nil || !h.Busy(id) {
			continue
		}
		item := deviceActivityConversation{ID: id}
		if conv, err := h.Store.GetConversation(c.Request.Context(), id); err == nil {
			item.Title = conv.Title
		}
		running = append(running, item)
	}
	c.JSON(http.StatusOK, gin.H{"running": running})
}

// PickFolder opens the native folder dialog on the caller's device.
func (h *DeviceHandler) PickFolder(c *gin.Context) {
	device, err := h.Store.GetDevice(c.Request.Context(), c.Param("id"))
	if err != nil || device.UserID != middleware.CurrentUserID(c) {
		c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
		return
	}
	if !h.Hub.Online(device.ID) {
		c.JSON(http.StatusConflict, gin.H{"error": service.ErrDeviceOffline.Error()})
		return
	}
	var req struct {
		Start string `json:"start"`
	}
	_ = c.ShouldBindJSON(&req)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
	defer cancel()
	raw, err := h.Hub.Call(ctx, device.ID, "pick_folder", map[string]string{"start": req.Start})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var res struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(raw, &res)
	c.JSON(http.StatusOK, gin.H{"path": res.Path})
}

type conversationDeviceView struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name"`
	WorkDir    string `json:"work_dir"`
	Online     bool   `json:"online"`
}

// GetConversationDevice returns the conversation's binding, or null.
func (h *DeviceHandler) GetConversationDevice(c *gin.Context) {
	conv, ok := loadOwnedConversation(c, h.Store, c.Param("id"))
	if !ok {
		return
	}
	b, ok := h.Hub.BindingFor(c.Request.Context(), h.Store, conv.ID)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"binding": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"binding": conversationDeviceView{
		DeviceID: b.DeviceID, DeviceName: b.Device.Name, WorkDir: b.WorkDir, Online: h.Hub.Online(b.DeviceID),
	}})
}

// SetConversationDevice binds a conversation to one of the caller's devices
// and a directory on it.
func (h *DeviceHandler) SetConversationDevice(c *gin.Context) {
	conv, ok := loadOwnedConversation(c, h.Store, c.Param("id"))
	if !ok {
		return
	}
	var req struct {
		DeviceID string `json:"device_id"`
		WorkDir  string `json:"work_dir"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if !h.deviceBindingMutable(c, conv.ID) {
		return
	}
	b, err := h.Hub.BindConversation(c.Request.Context(), middleware.CurrentUserID(c), conv.ID, req.DeviceID, req.WorkDir)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"binding": conversationDeviceView{
		DeviceID: b.DeviceID, DeviceName: b.Device.Name, WorkDir: b.WorkDir, Online: true,
	}})
}

// deviceBindingMutable reports whether a conversation's run location may still
// change, writing a 409 when it may not. Once a conversation bound to a
// folder on a computer has a model reply (Store.HasModelReply), its history
// refers to that folder's files and commands, so the folder is fixed: it can
// be viewed but neither swapped nor dropped back to the server.
func (h *DeviceHandler) deviceBindingMutable(c *gin.Context, convID string) bool {
	ctx := c.Request.Context()
	if _, err := h.Store.GetConversationDevice(ctx, convID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return true
		}
		respondInternalError(c, "devices", err)
		return false
	}
	started, err := h.Store.HasModelReply(ctx, convID)
	if err != nil {
		respondInternalError(c, "devices", err)
		return false
	}
	if started {
		c.JSON(http.StatusConflict, gin.H{"error": "cannot change the folder after the conversation has started"})
		return false
	}
	return true
}

// ClearConversationDevice returns the conversation to server execution.
func (h *DeviceHandler) ClearConversationDevice(c *gin.Context) {
	conv, ok := loadOwnedConversation(c, h.Store, c.Param("id"))
	if !ok {
		return
	}
	if !h.deviceBindingMutable(c, conv.ID) {
		return
	}
	if err := h.Hub.UnbindConversation(c.Request.Context(), conv.ID); err != nil {
		respondInternalError(c, "devices", err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ListConversationDeviceFiles lists a directory of the conversation's bound
// folder for the workspace panel. Paths are relative to that folder.
func (h *DeviceHandler) ListConversationDeviceFiles(c *gin.Context) {
	conv, ok := loadOwnedConversation(c, h.Store, c.Param("id"))
	if !ok {
		return
	}
	listing, err := h.Hub.ListDeviceDir(c.Request.Context(), h.Store, conv.ID, c.Query("path"))
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, listing)
}

// ReadConversationDeviceFile streams a file of the conversation's bound
// folder. The bytes come from the user's computer, so they are never served
// as an active document of this origin: HTML and other script-capable types
// go out as text, and every response carries a sandbox CSP.
func (h *DeviceHandler) ReadConversationDeviceFile(c *gin.Context) {
	conv, ok := loadOwnedConversation(c, h.Store, c.Param("id"))
	if !ok {
		return
	}
	file, err := h.Hub.ReadDeviceFile(c.Request.Context(), h.Store, conv.ID, c.Query("path"))
	if err != nil {
		respondServiceError(c, err)
		return
	}
	name := path.Base(strings.ReplaceAll(c.Query("path"), "\\", "/"))
	c.Header("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self' data: blob:; style-src 'unsafe-inline'")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "no-store")
	disposition := "inline"
	if c.Query("download") != "" {
		disposition = "attachment"
	}
	c.Header("Content-Disposition", disposition+"; filename*=UTF-8''"+url.PathEscape(name))
	c.Header("Last-Modified", file.Modified.Format(http.TimeFormat))
	c.Data(http.StatusOK, deviceFileContentType(name, file.Data), file.Data)
}

// deviceFileContentType picks a passive content type: images, PDF, audio and
// video keep theirs, everything else is served as text or octets.
func deviceFileContentType(name string, data []byte) string {
	ct := mime.TypeByExtension(strings.ToLower(path.Ext(name)))
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	base := strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])
	switch {
	case base == "image/svg+xml":
		return "text/plain; charset=utf-8"
	case strings.HasPrefix(base, "image/"), strings.HasPrefix(base, "audio/"), strings.HasPrefix(base, "video/"), base == "application/pdf":
		return base
	case strings.HasPrefix(http.DetectContentType(data), "text/"):
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// localOnly admits requests that come straight from a process on this host.
// The MCP endpoint is for the agents DayMug spawns; a request a reverse proxy
// forwarded (it adds X-Forwarded-For) did not come from one of them, even
// though it too arrives over loopback.
func localOnly(r *http.Request) bool {
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "" || r.Header.Get("Forwarded") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

// MCP is a minimal Streamable-HTTP MCP server (JSON responses, no SSE stream)
// exposing the bound device's tools to the conversation the token names.
func (h *DeviceHandler) MCP(c *gin.Context) {
	if !localOnly(c.Request) {
		c.Status(http.StatusNotFound)
		return
	}
	convID, ok := h.Hub.VerifyMCPToken(bearerToken(c.Request))
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	switch c.Request.Method {
	case http.MethodDelete:
		c.Status(http.StatusOK)
		return
	case http.MethodGet:
		// No server-initiated stream.
		c.Status(http.StatusMethodNotAllowed)
		return
	}
	body, err := readLimited(c.Request, deviceMCPBodyLimit)
	if err != nil {
		c.JSON(http.StatusBadRequest, jsonRPCResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &jsonRPCError{Code: -32700, Message: "parse error"}})
		return
	}
	trimmed := strings.TrimSpace(string(body))
	batch := strings.HasPrefix(trimmed, "[")
	var reqs []jsonRPCRequest
	if batch {
		err = json.Unmarshal(body, &reqs)
	} else {
		var one jsonRPCRequest
		err = json.Unmarshal(body, &one)
		reqs = []jsonRPCRequest{one}
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, jsonRPCResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &jsonRPCError{Code: -32700, Message: "parse error"}})
		return
	}
	var out []jsonRPCResponse
	for _, req := range reqs {
		if len(req.ID) == 0 {
			continue // notification
		}
		out = append(out, h.mcpDispatch(c.Request.Context(), convID, req))
	}
	if len(out) == 0 {
		c.Status(http.StatusAccepted)
		return
	}
	if batch {
		c.JSON(http.StatusOK, out)
		return
	}
	c.JSON(http.StatusOK, out[0])
}

func (h *DeviceHandler) mcpDispatch(ctx context.Context, convID string, req jsonRPCRequest) jsonRPCResponse {
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		resp.Result = map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": service.DeviceMCPServerName, "version": h.Version},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		binding, ok := h.Hub.BindingFor(ctx, h.Store, convID)
		tools := []service.DeviceTool{}
		if ok {
			tools = h.Hub.Tools(ctx, binding.DeviceID)
		}
		resp.Result = map[string]any{"tools": tools}
	case "tools/call":
		resp.Result = h.mcpCallTool(ctx, convID, req.Params)
	default:
		resp.Error = &jsonRPCError{Code: -32601, Message: "method not found"}
	}
	return resp
}

func (h *DeviceHandler) mcpCallTool(ctx context.Context, convID string, params json.RawMessage) any {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return service.DeviceToolError("invalid tool call")
	}
	raw, _ := h.Hub.CallTool(ctx, h.Store, convID, p.Name, p.Arguments)
	return raw
}

func readLimited(r *http.Request, limit int64) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(nil, r.Body, limit))
}
