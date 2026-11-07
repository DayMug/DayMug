package codexapp

// Device-bound turns (DayMug-Client) on the app-server.
//
// The agent keeps running here; only its file and shell tools move to the
// user's computer. Codex has no per-thread switch that removes every built-in
// touching this host, so a bound turn runs on its own app-server process
// (runtimeKey's device dimension) started with those built-ins disabled
// process-wide: the shell (`exec_command`/`write_stdin`), `view_image`, and —
// through a model catalog whose `apply_patch_tool_type` is cleared —
// `apply_patch`. Sub-agents spawned on that process inherit the same absence.
// The device's tools are registered as thread dynamicTools; the model's calls
// arrive as `item/tool/call` requests that this adapter answers through
// RunRequest.ToolRoute.Call.
//
// Dynamic tools are fixed when a thread starts (thread/resume and thread/fork
// cannot change them), so a conversation that moves between the server and a
// computer starts a new thread whose tool set matches, carrying the earlier
// user/assistant messages over with thread/inject_items.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// deviceDisabledFeatures are the Codex features whose tools reach this host.
var deviceDisabledFeatures = []string{"shell_tool", "unified_exec", "view_image"}

// deviceCatalogName is the model catalog a device server loads, written into
// the account's CODEX_HOME — the one directory every isolation layer already
// exposes to the app-server.
const deviceCatalogName = "daymug-device-model-catalog.json"

// errHostToolInDeviceTurn fails a bound turn closed if Codex ever runs one of
// its own host tools there (a Codex upgrade re-enabling one, say), rather than
// let it keep working on the server.
var errHostToolInDeviceTurn = errors.New("codex used a server-side tool in a conversation bound to a computer; the turn was stopped. Upgrade DayMug or switch the conversation back to the server")

// dynamicToolCallRequest is the app-server request that runs a dynamic tool.
const dynamicToolCallRequest = "item/tool/call"

func deviceTurn(opts agent.RunRequest) bool { return opts.ToolRoute != nil }

// usesHostTool reports a notification of Codex starting one of its own
// built-ins that act on this host.
func usesHostTool(msg wireMessage) bool {
	if msg.Method != "item/started" {
		return false
	}
	var p struct {
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	if json.Unmarshal(msg.Params, &p) != nil {
		return false
	}
	switch p.Item.Type {
	case "commandExecution", "fileChange", "imageView":
		return true
	}
	return false
}

// validateDeviceRoute rejects a route this transport cannot serve: without the
// tools' schemas there is nothing to register, and without Call nothing to
// answer the model with.
func validateDeviceRoute(route *agent.ToolRoute) error {
	if route.Call == nil {
		return agent.ErrToolRouteUnsupported
	}
	if len(route.Tools) == 0 {
		return errors.New("DayMug-Client on the bound computer has not connected since the server started, so its tools are unknown; open DayMug-Client and retry")
	}
	return nil
}

// bundledCatalog is the model catalog shipped inside the codex binary. A seam
// so tests don't need a codex install.
var bundledCatalog = loadBundledCatalog

var bundled struct {
	once sync.Once
	raw  []byte
	err  error
}

func loadBundledCatalog() ([]byte, error) {
	bundled.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// --bundled: no network, no account — the catalog this binary ships.
		cmd := exec.CommandContext(ctx, agent.ResolveAgentBinary(codexBinary), "debug", "models", "--bundled")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		bundled.raw, bundled.err = cmd.Output()
		if bundled.err != nil {
			bundled.err = fmt.Errorf("read codex model catalog: %w: %s", bundled.err, strings.TrimSpace(stderr.String()))
		}
	})
	return bundled.raw, bundled.err
}

// deviceCatalog strips apply_patch from every model in a catalog. Every other
// field is kept as Codex wrote it.
func deviceCatalog(raw []byte) ([]byte, error) {
	var catalog map[string]json.RawMessage
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, fmt.Errorf("parse codex model catalog: %w", err)
	}
	var models []map[string]json.RawMessage
	if err := json.Unmarshal(catalog["models"], &models); err != nil || len(models) == 0 {
		return nil, errors.New("codex model catalog has no models")
	}
	for _, m := range models {
		m["apply_patch_tool_type"] = json.RawMessage("null")
	}
	encoded, err := json.Marshal(models)
	if err != nil {
		return nil, err
	}
	catalog["models"] = encoded
	return json.Marshal(catalog)
}

// deviceServerArgs are the extra app-server flags of a device server.
func deviceServerArgs(opts agent.RunRequest) ([]string, error) {
	raw, err := bundledCatalog()
	if err != nil {
		return nil, err
	}
	catalog, err := deviceCatalog(raw)
	if err != nil {
		return nil, err
	}
	home := codexHome(opts)
	if home == "" {
		return nil, errors.New("cannot locate CODEX_HOME for the device model catalog")
	}
	path := filepath.Join(home, deviceCatalogName)
	if err := writeFileAtomic(path, catalog); err != nil {
		return nil, fmt.Errorf("write device model catalog: %w", err)
	}
	args := []string{
		"-c", "model_catalog_json=" + strconv.Quote(path),
		// The server's working directory is not where a bound conversation
		// works; its AGENTS.md must not steer the turn. The computer's own
		// instruction files arrive in developerInstructions instead.
		"-c", "project_doc_max_bytes=0",
	}
	for _, f := range deviceDisabledFeatures {
		args = append(args, "--disable", f)
	}
	return args, nil
}

func codexHome(opts agent.RunRequest) string {
	if opts.ConfigDir != "" {
		return opts.ConfigDir
	}
	if v := opts.AccountEnv["CODEX_HOME"]; v != "" {
		return v
	}
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".codex")
	}
	return ""
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// dynamicToolSpecs is the thread/start dynamicTools list for a route.
func dynamicToolSpecs(route *agent.ToolRoute) []map[string]any {
	specs := make([]map[string]any, 0, len(route.Tools))
	for _, t := range route.Tools {
		specs = append(specs, map[string]any{
			"type": "function", "name": t.Name, "description": t.Description, "inputSchema": t.InputSchema,
		})
	}
	return specs
}

// rolloutToolProfile reports whether a thread's rollout was started with
// dynamic tools. ok is false when the rollout can't be read, in which case the
// caller keeps the thread: a missing rollout fails the resume on its own.
func rolloutToolProfile(path string) (hasDynamicTools, ok bool) {
	if path == "" {
		return false, false
	}
	f, err := os.Open(path)
	if err != nil {
		return false, false
	}
	defer func() { _ = f.Close() }()
	reader := bufio.NewReaderSize(f, 64*1024)
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return false, false
	}
	var first struct {
		Type    string `json:"type"`
		Payload struct {
			DynamicTools []json.RawMessage `json:"dynamic_tools"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &first) != nil || first.Type != "session_meta" {
		return false, false
	}
	return len(first.Payload.DynamicTools) > 0, true
}

// carriedHistoryLimit bounds how much earlier conversation a replacement
// thread inherits; the newest messages are kept.
const carriedHistoryLimit = 200 * 1024

// carriedHistory extracts the user and assistant messages of a rollout as
// Responses API items for thread/inject_items. Developer messages, injected
// context (AGENTS.md, environment) and tool traffic are left behind: the new
// thread brings its own, and the old tools no longer exist. A compaction
// replaces everything before it with its replacement history, as it does for
// the model.
func carriedHistory(path string) []map[string]any {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var items []map[string]any
	reader := bufio.NewReaderSize(f, 1024*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var rec struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if json.Unmarshal(line, &rec) == nil {
				switch rec.Type {
				case "response_item":
					if it := carriedMessage(rec.Payload); it != nil {
						items = append(items, it)
					}
				case "compacted":
					var c struct {
						ReplacementHistory []json.RawMessage `json:"replacement_history"`
					}
					if json.Unmarshal(rec.Payload, &c) == nil && c.ReplacementHistory != nil {
						items = items[:0]
						for _, raw := range c.ReplacementHistory {
							if it := carriedMessage(raw); it != nil {
								items = append(items, it)
							}
						}
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	size := 0
	start := len(items)
	for start > 0 {
		n := messageSize(items[start-1])
		if size+n > carriedHistoryLimit {
			break
		}
		size += n
		start--
	}
	return items[start:]
}

func carriedMessage(raw json.RawMessage) map[string]any {
	var m struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Type != "message" || (m.Role != "user" && m.Role != "assistant") {
		return nil
	}
	want := "input_text"
	if m.Role == "assistant" {
		want = "output_text"
	}
	var content []map[string]string
	for _, c := range m.Content {
		if c.Text == "" || (c.Type != "input_text" && c.Type != "output_text") {
			continue
		}
		if m.Role == "user" && injectedContext(c.Text) {
			continue
		}
		content = append(content, map[string]string{"type": want, "text": c.Text})
	}
	if len(content) == 0 {
		return nil
	}
	return map[string]any{"type": "message", "role": m.Role, "content": content}
}

// injectedContext recognises the user-role items Codex writes itself (project
// docs, environment and skill context) rather than the user's own words.
func injectedContext(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "# AGENTS.md instructions") || strings.HasPrefix(t, "<")
}

func messageSize(item map[string]any) int {
	n := 0
	if content, ok := item["content"].([]map[string]string); ok {
		for _, c := range content {
			n += len(c["text"])
		}
	}
	return n
}

// toolCallResponse runs one item/tool/call through the route and builds the
// app-server's DynamicToolCallResponse.
func toolCallResponse(ctx context.Context, route *agent.ToolRoute, params json.RawMessage) map[string]any {
	var p struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(params, &p) != nil || p.Tool == "" {
		return toolFailure("invalid tool call")
	}
	if route == nil || route.Call == nil {
		return toolFailure("tool " + p.Tool + " is only available while this conversation runs on a computer")
	}
	raw, err := route.Call(ctx, p.Tool, p.Arguments)
	if err != nil {
		return toolFailure(err.Error())
	}
	return mcpToDynamicResult(raw)
}

func toolFailure(msg string) map[string]any {
	return map[string]any{"success": false, "contentItems": []map[string]string{{"type": "inputText", "text": msg}}}
}

// mcpToDynamicResult converts an MCP CallToolResult (what the device returns)
// into DynamicToolCallResponse content items.
func mcpToDynamicResult(raw json.RawMessage) map[string]any {
	var res struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return toolFailure("the computer returned an unreadable tool result")
	}
	items := make([]map[string]string, 0, len(res.Content))
	for _, c := range res.Content {
		switch c.Type {
		case "text":
			items = append(items, map[string]string{"type": "inputText", "text": c.Text})
		case "image":
			if c.Data != "" && c.MimeType != "" {
				items = append(items, map[string]string{"type": "inputImage", "imageUrl": "data:" + c.MimeType + ";base64," + c.Data})
			}
		}
	}
	if len(items) == 0 {
		items = append(items, map[string]string{"type": "inputText", "text": ""})
	}
	return map[string]any{"success": !res.IsError, "contentItems": items}
}
