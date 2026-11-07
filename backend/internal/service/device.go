package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// DeviceHub connects conversations to paired desktop clients (DayMug-Client).
//
// The agent keeps running on the server with the server's credentials and
// session logs; only its file and shell tools move. A bound conversation's
// turn gets an MCP server (served by the handler layer, see DeviceMCPServer)
// whose tool calls the hub relays over the client's outbound WebSocket. The
// hub is transport-agnostic: the handler owns the socket and hands the hub a
// send function plus every frame it reads.
type DeviceHub struct {
	store store.DeviceStore
	// secret signs per-conversation MCP tokens. Random per process: a token
	// only has to outlive the agent processes this process spawned.
	secret []byte

	mu       sync.Mutex
	sessions map[string]*DeviceSession
	pairs    map[string]pendingPair
	// lastTools remembers each device's advertised tools past a disconnect,
	// so an agent that starts while the client is briefly away still gets
	// the tool list (calls then wait for the reconnect).
	lastTools map[string][]DeviceTool
	// online is closed and replaced whenever a session registers, so callers
	// waiting for a reconnect wake up without polling.
	online chan struct{}

	now func() time.Time
}

// DeviceMCPServerName is the MCP server key bound conversations get; their
// tools reach the model as mcp__daymug_local__<Tool>.
const DeviceMCPServerName = agent.DeviceMCPServer

// deviceRoutedTools are the built-ins that touch the host filesystem or run
// processes. A bound conversation must not reach the server through any of
// them, so they are removed; the ones the client implements are additionally
// aliased so a model-emitted built-in name lands on the device.
var deviceRoutedTools = []string{
	"Bash", "BashOutput", "KillShell", "KillBash", "Monitor",
	"Read", "Write", "Edit", "MultiEdit", "NotebookEdit",
	"Glob", "Grep", "LS", "EnterWorktree", "ExitWorktree",
}

const (
	pairTTL = 10 * time.Minute
	// deviceReconnectWait bounds how long a tool call waits for a client that
	// dropped off (laptop lid, network blip) before failing.
	deviceReconnectWait = 15 * time.Second
)

// ErrDeviceOffline is the tool error a bound conversation sees while its
// client is not connected.
var ErrDeviceOffline = errors.New("DayMug-Client on the bound computer is not connected")

type pendingPair struct {
	userID  string
	name    string
	os      string
	expires time.Time
}

// DeviceTool is one tool a client advertises in its hello. The client owns the
// tool contract, so a client update can add a tool without a server release.
type DeviceTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// DeviceHello is the first frame a client sends after connecting.
type DeviceHello struct {
	// Nonce is the per-launch secret the client also injects into the page
	// it hosts; it ties a browser session to this connection.
	Nonce    string       `json:"nonce"`
	Version  string       `json:"version"`
	OS       string       `json:"os"`
	Hostname string       `json:"hostname"`
	Home     string       `json:"home"`
	Tools    []DeviceTool `json:"tools"`
}

// DeviceFrame is the envelope of every message on the device socket.
//
//	server → client: call {id, method, params} · cancel {id} · rebind {device_id, token}
//	client → server: hello {hello} · result {id, result | error}
type DeviceFrame struct {
	Type     string          `json:"type"`
	ID       string          `json:"id,omitempty"`
	Method   string          `json:"method,omitempty"`
	Params   json.RawMessage `json:"params,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    string          `json:"error,omitempty"`
	Hello    *DeviceHello    `json:"hello,omitempty"`
	DeviceID string          `json:"device_id,omitempty"`
	Token    string          `json:"token,omitempty"`
}

// DeviceSession is one live client connection.
type DeviceSession struct {
	hub    *DeviceHub
	device store.Device
	hello  DeviceHello
	send   func(DeviceFrame) error
	done   chan struct{}

	mu      sync.Mutex
	pending map[string]chan DeviceFrame
	closed  bool
}

// NewDeviceHub builds a hub over the device store.
func NewDeviceHub(s store.DeviceStore) *DeviceHub {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic(fmt.Sprintf("device hub: read random: %v", err))
	}
	return &DeviceHub{
		store:     s,
		secret:    secret,
		sessions:  map[string]*DeviceSession{},
		pairs:     map[string]pendingPair{},
		lastTools: map[string][]DeviceTool{},
		online:    make(chan struct{}),
		now:       time.Now,
	}
}

// HashDeviceToken is how a client's bearer token is stored and looked up.
func HashDeviceToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newDeviceToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("device token: read random: %v", err))
	}
	return "dmd_" + base64.RawURLEncoding.EncodeToString(b)
}

// Authenticate resolves a client's bearer token to its device.
func (h *DeviceHub) Authenticate(ctx context.Context, token string) (store.Device, error) {
	if !strings.HasPrefix(token, "dmd_") {
		return store.Device{}, store.ErrNotFound
	}
	return h.store.GetDeviceByTokenHash(ctx, HashDeviceToken(token))
}

func validNonce(nonce string) bool {
	return len(nonce) >= 32 && len(nonce) <= 128
}

func trimField(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// BeginPair is called by the logged-in page inside DayMug-Client with the
// nonce its host injected. If the client is already connected (as another
// user's device, or with a revoked token being replaced) the new credentials
// are pushed down that connection; otherwise they wait for the client to
// Claim them.
func (h *DeviceHub) BeginPair(ctx context.Context, userID, nonce, name, osName string) error {
	if !validNonce(nonce) {
		return BadRequest("invalid pairing nonce")
	}
	name, osName = trimField(name, 100), trimField(osName, 40)
	h.mu.Lock()
	var target *DeviceSession
	for _, s := range h.sessions {
		if s.hello.Nonce == nonce {
			target = s
			break
		}
	}
	if target == nil {
		h.prunePairsLocked()
		h.pairs[nonce] = pendingPair{userID: userID, name: name, os: osName, expires: h.now().Add(pairTTL)}
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()
	if target.device.UserID == userID {
		return nil
	}
	if osName == "" {
		osName = target.hello.OS
	}
	device, token, err := h.createDevice(ctx, userID, name, osName)
	if err != nil {
		return err
	}
	return target.send(DeviceFrame{Type: "rebind", DeviceID: device.ID, Token: token})
}

// Claim hands a client the credentials its page requested. ok is false while
// nothing is pending for the nonce, which the client treats as "keep polling".
func (h *DeviceHub) Claim(ctx context.Context, nonce string) (device store.Device, token string, ok bool, err error) {
	if !validNonce(nonce) {
		return store.Device{}, "", false, nil
	}
	h.mu.Lock()
	h.prunePairsLocked()
	pair, found := h.pairs[nonce]
	delete(h.pairs, nonce)
	h.mu.Unlock()
	if !found {
		return store.Device{}, "", false, nil
	}
	device, token, err = h.createDevice(ctx, pair.userID, pair.name, pair.os)
	if err != nil {
		return store.Device{}, "", false, err
	}
	return device, token, true, nil
}

func (h *DeviceHub) prunePairsLocked() {
	now := h.now()
	for k, p := range h.pairs {
		if now.After(p.expires) {
			delete(h.pairs, k)
		}
	}
}

func (h *DeviceHub) createDevice(ctx context.Context, userID, name, osName string) (store.Device, string, error) {
	token := newDeviceToken()
	if name == "" {
		name = "DayMug-Client"
	}
	device, err := h.store.CreateDevice(ctx, store.Device{UserID: userID, Name: name, OS: osName, TokenHash: HashDeviceToken(token)})
	if err != nil {
		return store.Device{}, "", err
	}
	return device, token, nil
}

// Register installs a freshly connected client, replacing any older
// connection of the same device.
func (h *DeviceHub) Register(device store.Device, hello DeviceHello, send func(DeviceFrame) error) *DeviceSession {
	s := &DeviceSession{hub: h, device: device, hello: hello, send: send, done: make(chan struct{}), pending: map[string]chan DeviceFrame{}}
	h.mu.Lock()
	old := h.sessions[device.ID]
	h.sessions[device.ID] = s
	h.lastTools[device.ID] = hello.Tools
	close(h.online)
	h.online = make(chan struct{})
	h.mu.Unlock()
	if old != nil {
		old.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.store.TouchDevice(ctx, device.ID, h.now())
	return s
}

// Done is closed when the session is closed (replaced, revoked or dropped).
func (s *DeviceSession) Done() <-chan struct{} { return s.done }

// Deliver routes a result frame read from the socket to its waiting call.
func (s *DeviceSession) Deliver(frame DeviceFrame) {
	if frame.Type != "result" || frame.ID == "" {
		return
	}
	s.mu.Lock()
	ch := s.pending[frame.ID]
	delete(s.pending, frame.ID)
	s.mu.Unlock()
	if ch != nil {
		ch <- frame
	}
}

// Close unregisters the session and fails its in-flight calls.
func (s *DeviceSession) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	pending := s.pending
	s.pending = map[string]chan DeviceFrame{}
	close(s.done)
	s.mu.Unlock()
	for _, ch := range pending {
		ch <- DeviceFrame{Type: "result", Error: ErrDeviceOffline.Error()}
	}
	h := s.hub
	h.mu.Lock()
	if h.sessions[s.device.ID] == s {
		delete(h.sessions, s.device.ID)
	}
	h.mu.Unlock()
}

func (s *DeviceSession) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	id := uuid.NewString()
	ch := make(chan DeviceFrame, 1)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrDeviceOffline
	}
	s.pending[id] = ch
	s.mu.Unlock()
	if err := s.send(DeviceFrame{Type: "call", ID: id, Method: method, Params: params}); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, ErrDeviceOffline
	}
	select {
	case frame := <-ch:
		if frame.Error != "" {
			return nil, errors.New(frame.Error)
		}
		return frame.Result, nil
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		_ = s.send(DeviceFrame{Type: "cancel", ID: id})
		return nil, ctx.Err()
	}
}

// session returns the live session of a device, waiting up to wait for a
// reconnect.
func (h *DeviceHub) session(ctx context.Context, deviceID string, wait time.Duration) (*DeviceSession, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		h.mu.Lock()
		s := h.sessions[deviceID]
		online := h.online
		h.mu.Unlock()
		if s != nil {
			return s, nil
		}
		select {
		case <-online:
		case <-timer.C:
			return nil, ErrDeviceOffline
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Call runs method on a device and returns its raw result.
func (h *DeviceHub) Call(ctx context.Context, deviceID, method string, params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	s, err := h.session(ctx, deviceID, deviceReconnectWait)
	if err != nil {
		return nil, err
	}
	return s.call(ctx, method, raw)
}

// Tools is the tool list of a device for an MCP tools/list: live when the
// client is (or soon becomes) connected, the last advertised list otherwise.
func (h *DeviceHub) Tools(ctx context.Context, deviceID string) []DeviceTool {
	if s, err := h.session(ctx, deviceID, deviceReconnectWait); err == nil && len(s.hello.Tools) > 0 {
		return s.hello.Tools
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if tools := h.lastTools[deviceID]; len(tools) > 0 {
		return tools
	}
	return []DeviceTool{}
}

// Hello returns what a connected device announced.
func (h *DeviceHub) Hello(deviceID string) (DeviceHello, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[deviceID]
	if s == nil {
		return DeviceHello{}, false
	}
	return s.hello, true
}

// Online reports whether a device has a live connection.
func (h *DeviceHub) Online(deviceID string) bool {
	_, ok := h.Hello(deviceID)
	return ok
}

// CurrentDevice resolves the client a page is hosted in: the connected device
// that announced nonce, if it belongs to userID.
func (h *DeviceHub) CurrentDevice(userID, nonce string) (store.Device, DeviceHello, bool) {
	if !validNonce(nonce) {
		return store.Device{}, DeviceHello{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.sessions {
		if s.hello.Nonce == nonce && s.device.UserID == userID {
			return s.device, s.hello, true
		}
	}
	return store.Device{}, DeviceHello{}, false
}

// Revoke deletes a user's device and drops its connection.
func (h *DeviceHub) Revoke(ctx context.Context, userID, deviceID string) error {
	if err := h.store.DeleteDevice(ctx, userID, deviceID); err != nil {
		return err
	}
	h.mu.Lock()
	s := h.sessions[deviceID]
	h.mu.Unlock()
	if s != nil {
		s.Close()
	}
	return nil
}

// MCPToken is the credential in a bound conversation's MCP URL. It names the
// conversation and nothing else; the device and directory are read from the
// binding on every call, so rebinding takes effect on a parked process too.
func (h *DeviceHub) MCPToken(conversationID string) string {
	mac := hmac.New(sha256.New, h.secret)
	_, _ = mac.Write([]byte("mcp:" + conversationID))
	return conversationID + "." + hex.EncodeToString(mac.Sum(nil))
}

// VerifyMCPToken returns the conversation a token was minted for.
func (h *DeviceHub) VerifyMCPToken(token string) (string, bool) {
	i := strings.LastIndexByte(token, '.')
	if i <= 0 {
		return "", false
	}
	convID := token[:i]
	if !hmac.Equal([]byte(h.MCPToken(convID)), []byte(token)) {
		return "", false
	}
	return convID, true
}

// DeviceBinding is a conversation's resolved device route.
type DeviceBinding struct {
	store.ConversationDevice
	Device store.Device
}

// BindingFor returns the conversation's device binding when it has one whose
// device still belongs to the conversation's human owner. A binding that
// fails the ownership check is treated as absent — a device can never be
// reached through a conversation of another user.
func (h *DeviceHub) BindingFor(ctx context.Context, s store.Store, convID string) (DeviceBinding, bool) {
	b, err := h.store.GetConversationDevice(ctx, convID)
	if err != nil {
		return DeviceBinding{}, false
	}
	device, err := h.store.GetDevice(ctx, b.DeviceID)
	if err != nil {
		return DeviceBinding{}, false
	}
	conv, err := s.GetConversation(ctx, convID)
	if err != nil {
		return DeviceBinding{}, false
	}
	owner, err := s.GetUser(ctx, conv.UserID)
	if err != nil || owner.Owner() != device.UserID {
		return DeviceBinding{}, false
	}
	return DeviceBinding{ConversationDevice: b, Device: device}, true
}

// deviceToolAliases maps the built-in names a model may still emit onto the
// client's tools, for the tools the client advertises.
func deviceToolAliases(tools []DeviceTool) map[string]string {
	advertised := map[string]bool{}
	for _, t := range tools {
		advertised[t.Name] = true
	}
	aliases := map[string]string{}
	for _, name := range deviceRoutedTools {
		if advertised[name] {
			aliases[name] = "mcp__" + DeviceMCPServerName + "__" + name
		}
	}
	return aliases
}

// defaultDeviceTools is what the alias table assumes while the client is
// offline at turn start (its hello is unknown); the client implements these.
var defaultDeviceTools = []DeviceTool{{Name: "Bash"}, {Name: "Read"}, {Name: "Write"}, {Name: "Edit"}, {Name: "Glob"}, {Name: "Grep"}}

// ApplyDeviceRoute rewires opts for a device-bound conversation: the server's
// file and shell built-ins are removed, the device's MCP server is added, and
// the system prompt describes the machine the tools actually reach. A
// conversation without a binding is left untouched — nothing about a plain
// web conversation changes.
func (h *DeviceHub) ApplyDeviceRoute(ctx context.Context, s store.Store, opts *agent.RunRequest, convID, mcpBaseURL string) bool {
	if h == nil || opts == nil || mcpBaseURL == "" {
		return false
	}
	binding, ok := h.BindingFor(ctx, s, convID)
	if !ok {
		return false
	}
	hello, online := h.Hello(binding.DeviceID)
	tools := hello.Tools
	if !online {
		tools = defaultDeviceTools
	}
	convTools := knownTools(tools, h.lastKnownTools(binding.DeviceID))
	opts.ToolRoute = &agent.ToolRoute{
		MCPServers: map[string]any{
			// The token rides in a header, not the URL, so access logs never
			// record it.
			DeviceMCPServerName: map[string]any{
				"type":    "http",
				"url":     strings.TrimRight(mcpBaseURL, "/") + "/api/devices/mcp",
				"headers": map[string]string{"Authorization": "Bearer " + h.MCPToken(convID)},
			},
		},
		Disallowed: append([]string(nil), deviceRoutedTools...),
		Aliases:    deviceToolAliases(tools),
		// The server's working directory is not where this conversation
		// works: its CLAUDE.md / .claude settings ("project", "local") must
		// not steer the turn. The device folder's files are loaded below.
		SettingSources: []string{"user"},
		Tools:          convTools,
		Call: func(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error) {
			return h.CallTool(ctx, s, convID, name, arguments)
		},
	}
	AppendSystem(opts, deviceSystemPrompt(binding, hello, online))
	AppendSystem(opts, h.deviceInstructionsPrompt(ctx, s, binding, online))
	return true
}

// lastKnownTools is the tool list a device last advertised, without waiting
// for it to reconnect.
func (h *DeviceHub) lastKnownTools(deviceID string) []DeviceTool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastTools[deviceID]
}

// knownTools is the schema-bearing tool list a native-tool transport registers
// for a turn: the live hello when the client is connected, the last advertised
// list otherwise. Empty when the client has never connected in this process.
func knownTools(live, last []DeviceTool) []agent.RoutedTool {
	src := live
	if len(src) == 0 {
		src = last
	}
	out := make([]agent.RoutedTool, 0, len(src))
	for _, t := range src {
		if t.Name == "" || len(t.InputSchema) == 0 {
			continue
		}
		out = append(out, agent.RoutedTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

// CallTool runs one of the bound device's tools for a conversation and returns
// the client's MCP CallToolResult. A transport-level failure comes back as an
// MCP error result, so the model sees why the call failed. The binding is
// re-read on every call: a revoked device or a rebinding takes effect on the
// next tool call, even inside a running turn.
func (h *DeviceHub) CallTool(ctx context.Context, s store.Store, convID, name string, arguments json.RawMessage) (json.RawMessage, error) {
	if name == "" {
		return DeviceToolError("invalid tool call"), nil
	}
	binding, ok := h.BindingFor(ctx, s, convID)
	if !ok {
		return DeviceToolError("this conversation is no longer bound to a computer"), nil
	}
	if len(arguments) == 0 || string(arguments) == "null" {
		arguments = json.RawMessage("{}")
	}
	raw, err := h.Call(ctx, binding.DeviceID, "tool", map[string]any{
		"name": name, "arguments": arguments, "cwd": binding.WorkDir,
	})
	if err != nil {
		return DeviceToolError(err.Error()), nil
	}
	return raw, nil
}

// DeviceToolError is an MCP CallToolResult reporting msg as a tool error.
func DeviceToolError(msg string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": msg}}, "isError": true})
	return raw
}

// deviceInstructionTimeout bounds how long a turn start waits for the client
// to collect the bound folder's instruction files.
const deviceInstructionTimeout = 5 * time.Second

// DeviceInstructionFile is one instruction file the client collected.
type DeviceInstructionFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// deviceInstructionsPrompt replaces the CLAUDE.md the SDK would have read
// from the server's working directory: the agent's DayMug-managed
// instructions, then the CLAUDE.md / AGENTS.md files the client found from the
// bound folder upwards. An offline or older client contributes nothing; the
// turn still runs.
func (h *DeviceHub) deviceInstructionsPrompt(ctx context.Context, s store.Store, b DeviceBinding, online bool) string {
	var sb strings.Builder
	if conv, err := s.GetConversation(ctx, b.ConversationID); err == nil {
		if u, err := s.GetUser(ctx, conv.UserID); err == nil && u.ManageClaudeMd && strings.TrimSpace(u.ClaudeMdContent) != "" {
			sb.WriteString("# Agent instructions\n\nConfigured for this agent in DayMug:\n\n")
			sb.WriteString(strings.TrimSpace(u.ClaudeMdContent))
			sb.WriteString("\n\n")
		}
	}
	if online {
		files := h.deviceInstructionFiles(ctx, b)
		if len(files) > 0 {
			sb.WriteString("# Project instructions from the user's computer\n\n")
			sb.WriteString("These files were found on the user's computer, from the outermost directory down to the working directory; later files are more specific and win a conflict. Follow them like CLAUDE.md.\n")
			for _, f := range files {
				sb.WriteString("\nContents of `" + f.Path + "`:\n\n")
				sb.WriteString(strings.TrimRight(f.Content, "\n"))
				sb.WriteString("\n")
			}
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (h *DeviceHub) deviceInstructionFiles(ctx context.Context, b DeviceBinding) []DeviceInstructionFile {
	callCtx, cancel := context.WithTimeout(ctx, deviceInstructionTimeout)
	defer cancel()
	raw, err := h.Call(callCtx, b.DeviceID, "project_instructions", map[string]string{"root": b.WorkDir})
	if err != nil {
		return nil
	}
	var res struct {
		Files []DeviceInstructionFile `json:"files"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return nil
	}
	return res.Files
}

func deviceSystemPrompt(b DeviceBinding, hello DeviceHello, online bool) string {
	osName := hello.OS
	if osName == "" {
		osName = b.Device.OS
	}
	var sb strings.Builder
	sb.WriteString("# Execution environment\n\n")
	sb.WriteString("You are working directly on the user's own computer")
	if osName != "" {
		sb.WriteString(" (" + osName)
		if hello.Hostname != "" {
			sb.WriteString(", host " + hello.Hostname)
		}
		sb.WriteString(")")
	}
	sb.WriteString(". Every file and shell operation goes through the Bash, Read, Write, Edit, Glob and Grep tools ")
	sb.WriteString("(listed as `mcp__" + DeviceMCPServerName + "__*` where tool names carry a server prefix), which run on that computer. ")
	sb.WriteString("No other tool can reach any filesystem or run a command.\n\n")
	sb.WriteString("- Working directory: `" + b.WorkDir + "`. Relative paths and commands resolve against it.\n")
	if hello.Home != "" {
		sb.WriteString("- Home directory: `" + hello.Home + "`.\n")
	}
	sb.WriteString("- The working directory, platform and git status reported elsewhere in this prompt describe a different host you cannot reach; ignore them in favour of this section.\n")
	sb.WriteString("- Files you create are already on the user's computer; refer to them by their path instead of publishing them as artifacts.\n")
	if !online {
		sb.WriteString("- The client was not connected when this turn started; if a tool reports it offline, ask the user to open DayMug-Client.\n")
	}
	return sb.String()
}

// DeviceMCPBaseURL is how agent processes on this host reach the server's own
// HTTP listener: loopback on the listen port, whatever interface it binds.
func DeviceMCPBaseURL(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	addr := cfg.ListenAddr()
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// BindConversation points a conversation at a directory on one of userID's
// devices. The device must be online: the directory is checked — and
// canonicalised — on the machine it lives on.
func (h *DeviceHub) BindConversation(ctx context.Context, userID, convID, deviceID, workDir string) (DeviceBinding, error) {
	workDir = strings.TrimSpace(workDir)
	if deviceID == "" || workDir == "" || len(workDir) > 4096 {
		return DeviceBinding{}, BadRequest("device_id and work_dir are required")
	}
	device, err := h.store.GetDevice(ctx, deviceID)
	if err != nil || device.UserID != userID {
		return DeviceBinding{}, NewServiceError(http.StatusNotFound, "device not found", err)
	}
	if !h.Online(device.ID) {
		return DeviceBinding{}, NewServiceError(http.StatusConflict, ErrDeviceOffline.Error(), nil)
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := h.Call(callCtx, device.ID, "check_dir", map[string]string{"path": workDir})
	if err != nil {
		return DeviceBinding{}, BadRequest(err.Error())
	}
	var checked struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(raw, &checked) != nil || checked.Path == "" {
		return DeviceBinding{}, NewServiceError(http.StatusBadGateway, "device returned no directory", nil)
	}
	b := store.ConversationDevice{ConversationID: convID, DeviceID: device.ID, WorkDir: checked.Path}
	if err := h.store.SetConversationDevice(ctx, b); err != nil {
		return DeviceBinding{}, err
	}
	return DeviceBinding{ConversationDevice: b, Device: device}, nil
}

// UnbindConversation returns a conversation to server execution.
func (h *DeviceHub) UnbindConversation(ctx context.Context, convID string) error {
	return h.store.ClearConversationDevice(ctx, convID)
}

// DeviceFileEntry is one row of a bound folder's listing, in the shape the
// workspace panel's file API uses.
type DeviceFileEntry struct {
	Name     string    `json:"name"`
	IsDir    bool      `json:"is_dir"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// DeviceDirListing is a bound folder's directory listing.
type DeviceDirListing struct {
	Root      string            `json:"root"`
	Path      string            `json:"path"`
	Entries   []DeviceFileEntry `json:"entries"`
	Truncated bool              `json:"truncated"`
}

// DeviceFile is a file read from a bound folder.
type DeviceFile struct {
	Size     int64
	Modified time.Time
	Data     []byte
}

// deviceFileTimeout bounds one file-panel request to the client.
const deviceFileTimeout = 30 * time.Second

// DeviceFileReadLimit caps a file the panel previews or downloads; it keeps a
// base64-encoded result well under the socket's read limit.
const DeviceFileReadLimit = 16 << 20

// cleanDevicePath normalises a panel path relative to the bound folder. Only
// forward-slash relative paths without ".." are accepted; the client confines
// the resolved path to the folder again, symlinks included.
func cleanDevicePath(p string) (string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" || p == "." {
		return ".", nil
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, ":") {
		return "", BadRequest("path must be relative to the bound folder")
	}
	parts := []string{}
	for _, part := range strings.Split(p, "/") {
		switch part {
		case "", ".":
		case "..":
			return "", BadRequest("path must stay inside the bound folder")
		default:
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return ".", nil
	}
	return strings.Join(parts, "/"), nil
}

func (h *DeviceHub) fileBinding(ctx context.Context, s store.Store, convID string) (DeviceBinding, error) {
	b, ok := h.BindingFor(ctx, s, convID)
	if !ok {
		return DeviceBinding{}, NewServiceError(http.StatusNotFound, "conversation is not bound to a computer", nil)
	}
	if !h.Online(b.DeviceID) {
		return DeviceBinding{}, NewServiceError(http.StatusConflict, ErrDeviceOffline.Error(), nil)
	}
	return b, nil
}

// deviceCallError maps a client failure: an unsupported method means the
// client predates the file panel.
func deviceCallError(err error) error {
	if strings.Contains(err.Error(), "unsupported method") {
		return NewServiceError(http.StatusNotImplemented, "update DayMug-Client to browse this computer's files", err)
	}
	return NewServiceError(http.StatusBadGateway, err.Error(), err)
}

// ListDeviceDir lists a directory of a conversation's bound folder.
func (h *DeviceHub) ListDeviceDir(ctx context.Context, s store.Store, convID, path string) (DeviceDirListing, error) {
	path, err := cleanDevicePath(path)
	if err != nil {
		return DeviceDirListing{}, err
	}
	b, err := h.fileBinding(ctx, s, convID)
	if err != nil {
		return DeviceDirListing{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, deviceFileTimeout)
	defer cancel()
	raw, err := h.Call(callCtx, b.DeviceID, "list_dir", map[string]string{"root": b.WorkDir, "path": path})
	if err != nil {
		return DeviceDirListing{}, deviceCallError(err)
	}
	var res struct {
		Entries []struct {
			Name       string `json:"name"`
			IsDir      bool   `json:"is_dir"`
			Size       int64  `json:"size"`
			ModifiedMS int64  `json:"modified_ms"`
		} `json:"entries"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return DeviceDirListing{}, NewServiceError(http.StatusBadGateway, "device returned an invalid listing", err)
	}
	out := DeviceDirListing{Root: b.WorkDir, Path: path, Entries: make([]DeviceFileEntry, 0, len(res.Entries)), Truncated: res.Truncated}
	for _, e := range res.Entries {
		out.Entries = append(out.Entries, DeviceFileEntry{Name: e.Name, IsDir: e.IsDir, Size: e.Size, Modified: time.UnixMilli(e.ModifiedMS).UTC()})
	}
	return out, nil
}

// ReadDeviceFile reads a file of a conversation's bound folder.
func (h *DeviceHub) ReadDeviceFile(ctx context.Context, s store.Store, convID, path string) (DeviceFile, error) {
	path, err := cleanDevicePath(path)
	if err != nil {
		return DeviceFile{}, err
	}
	if path == "." {
		return DeviceFile{}, BadRequest("path is a directory")
	}
	b, err := h.fileBinding(ctx, s, convID)
	if err != nil {
		return DeviceFile{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, deviceFileTimeout)
	defer cancel()
	raw, err := h.Call(callCtx, b.DeviceID, "read_file", map[string]any{"root": b.WorkDir, "path": path, "max_bytes": DeviceFileReadLimit})
	if err != nil {
		return DeviceFile{}, deviceCallError(err)
	}
	var res struct {
		Size       int64  `json:"size"`
		ModifiedMS int64  `json:"modified_ms"`
		Data       string `json:"data"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return DeviceFile{}, NewServiceError(http.StatusBadGateway, "device returned an invalid file", err)
	}
	data, err := base64.StdEncoding.DecodeString(res.Data)
	if err != nil {
		return DeviceFile{}, NewServiceError(http.StatusBadGateway, "device returned an invalid file", err)
	}
	return DeviceFile{Size: int64(len(data)), Modified: time.UnixMilli(res.ModifiedMS).UTC(), Data: data}, nil
}
