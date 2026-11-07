package codexapp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

const fixtureCatalog = `{"models":[{"slug":"gpt-5.6-sol","apply_patch_tool_type":"freeform","shell_type":"unified_exec"},{"slug":"gpt-5.6-luna","apply_patch_tool_type":"function"}],"etag":"x"}`

func stubCatalog(t *testing.T) {
	t.Helper()
	prev := bundledCatalog
	bundledCatalog = func() ([]byte, error) { return []byte(fixtureCatalog), nil }
	t.Cleanup(func() { bundledCatalog = prev })
}

type recordedCall struct {
	name string
	args string
}

func deviceRoute(calls *[]recordedCall) *agent.ToolRoute {
	return &agent.ToolRoute{
		Tools: []agent.RoutedTool{{Name: "Bash", Description: "Run a command", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Call: func(_ context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
			*calls = append(*calls, recordedCall{name: name, args: string(args)})
			return json.RawMessage(`{"content":[{"type":"text","text":"/home/me/project"}]}`), nil
		},
	}
}

func drain(ch <-chan agent.StreamEvent) []agent.StreamEvent {
	var events []agent.StreamEvent
	for e := range ch {
		events = append(events, e)
	}
	return events
}

func TestDeviceCatalogClearsApplyPatchAndKeepsTheRest(t *testing.T) {
	out, err := deviceCatalog([]byte(fixtureCatalog))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Models []map[string]any `json:"models"`
		Etag   string           `json:"etag"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 2 || got.Etag != "x" {
		t.Fatalf("catalog = %s", out)
	}
	for _, m := range got.Models {
		if v, ok := m["apply_patch_tool_type"]; !ok || v != nil {
			t.Fatalf("model %v keeps apply_patch_tool_type", m["slug"])
		}
	}
	if got.Models[0]["shell_type"] != "unified_exec" {
		t.Fatalf("unrelated fields must survive: %v", got.Models[0])
	}
	if _, err := deviceCatalog([]byte(`{"models":[]}`)); err == nil {
		t.Fatal("an empty catalog must fail the device server rather than start one with defaults")
	}
}

func TestDeviceTurnRunsOnItsOwnServerAndAnswersToolCalls(t *testing.T) {
	stubCatalog(t)
	home := t.TempDir()
	spawner := &scriptedSpawner{deviceToolCall: true}
	backend := NewBackend()
	var calls []recordedCall
	ch := make(chan agent.StreamEvent, 32)
	err := backend.RunWithSession(context.Background(), "where am I", "/workspace", agent.RunRequest{
		ConfigDir: home, Spawner: spawner, ToolRoute: deviceRoute(&calls),
	}, ch)
	if err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	events := drain(ch)

	argv := strings.Join(spawner.argv, " ")
	for _, want := range []string{"--disable shell_tool", "--disable unified_exec", "--disable view_image", "project_doc_max_bytes=0", "model_catalog_json="} {
		if !strings.Contains(argv, want) {
			t.Fatalf("device server argv %q lacks %q", argv, want)
		}
	}
	if _, err := os.Stat(filepath.Join(home, deviceCatalogName)); err != nil {
		t.Fatalf("device catalog not written into CODEX_HOME: %v", err)
	}
	start := paramsOf(t, spawner, "thread/start")
	tools, _ := start["dynamicTools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "Bash" {
		t.Fatalf("dynamicTools = %#v", start["dynamicTools"])
	}
	if start["sandbox"] != sandboxModeReadOnly {
		t.Fatalf("device thread sandbox = %v, want read-only", start["sandbox"])
	}
	if len(calls) != 1 || calls[0].name != "Bash" || calls[0].args != `{"command":"pwd"}` {
		t.Fatalf("route calls = %#v", calls)
	}
	result, _ := spawner.toolAnswer["result"].(map[string]any)
	items, _ := result["contentItems"].([]any)
	if result["success"] != true || len(items) != 1 || items[0].(map[string]any)["text"] != "/home/me/project" {
		t.Fatalf("tool answer = %#v", spawner.toolAnswer)
	}
	var sawUse, sawResult bool
	for _, e := range events {
		switch e.Kind {
		case agent.KindToolUseStart:
			sawUse = strings.Contains(e.Content, `"name":"Bash"`)
		case agent.KindToolResult:
			sawResult = strings.Contains(e.Content, "/home/me/project")
		}
	}
	if !sawUse || !sawResult {
		t.Fatalf("Bash card events missing: %#v", events)
	}

	// A server-side turn on the same account must not share the device server.
	plain := make(chan agent.StreamEvent, 32)
	spawner.mu.Lock()
	spawner.deviceToolCall = false
	spawner.mu.Unlock()
	if err := backend.RunWithSession(context.Background(), "hi", "/workspace", agent.RunRequest{ConfigDir: home, Spawner: spawner}, plain); err != nil {
		t.Fatal(err)
	}
	drain(plain)
	if spawner.spawnCount != 2 {
		t.Fatalf("spawn count = %d, want a separate server for the plain turn", spawner.spawnCount)
	}
	if strings.Contains(strings.Join(spawner.argv, " "), "--disable shell_tool") {
		t.Fatalf("plain server inherited device flags: %v", spawner.argv)
	}
}

func TestDeviceTurnStopsWhenCodexUsesAHostTool(t *testing.T) {
	stubCatalog(t)
	spawner := &scriptedSpawner{hostToolInTurn: true}
	var calls []recordedCall
	ch := make(chan agent.StreamEvent, 32)
	err := NewBackend().RunWithSession(context.Background(), "x", "/workspace", agent.RunRequest{
		ConfigDir: t.TempDir(), Spawner: spawner, ToolRoute: deviceRoute(&calls),
	}, ch)
	drain(ch)
	if !errors.Is(err, errHostToolInDeviceTurn) {
		t.Fatalf("err = %v, want errHostToolInDeviceTurn", err)
	}
	if !containsMethod(spawner.requests, "turn/interrupt") {
		t.Fatal("the turn must be interrupted")
	}
}

func TestDeviceRouteWithoutKnownToolsIsRefused(t *testing.T) {
	ch := make(chan agent.StreamEvent, 1)
	err := NewBackend().RunWithSession(context.Background(), "x", "/workspace", agent.RunRequest{
		Spawner: &scriptedSpawner{}, ToolRoute: &agent.ToolRoute{Call: func(context.Context, string, json.RawMessage) (json.RawMessage, error) { return nil, nil }},
	}, ch)
	if err == nil || !strings.Contains(err.Error(), "DayMug-Client") {
		t.Fatalf("err = %v", err)
	}
	if err := NewBackend().RunWithSession(context.Background(), "x", "/workspace", agent.RunRequest{
		Spawner: &scriptedSpawner{}, ToolRoute: &agent.ToolRoute{},
	}, make(chan agent.StreamEvent, 1)); !errors.Is(err, agent.ErrToolRouteUnsupported) {
		t.Fatalf("a route with no Call must stay unsupported, got %v", err)
	}
}

func writeRollout(t *testing.T, home, threadID string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "11", "07")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-11-07T00-00-00-"+threadID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMovingAConversationToAComputerStartsAThreadWithItsHistory(t *testing.T) {
	stubCatalog(t)
	home := t.TempDir()
	writeRollout(t, home, "old-thread",
		`{"type":"session_meta","payload":{"id":"old-thread","dynamic_tools":null}}`,
		`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"base"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /srv"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":"{}"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixed"}]}}`,
	)
	spawner := &scriptedSpawner{}
	var calls []recordedCall
	ch := make(chan agent.StreamEvent, 32)
	err := NewBackend().RunWithSession(context.Background(), "next", "/workspace", agent.RunRequest{
		ConfigDir: home, Spawner: spawner, SessionID: "old-thread", IsResume: true, ToolRoute: deviceRoute(&calls),
	}, ch)
	if err != nil {
		t.Fatal(err)
	}
	events := drain(ch)
	if containsMethod(spawner.requests, "thread/resume") {
		t.Fatal("a thread without the device tools must not be resumed")
	}
	if _, ok := paramsOf(t, spawner, "thread/start")["dynamicTools"]; !ok {
		t.Fatal("replacement thread lacks dynamicTools")
	}
	inject := paramsOf(t, spawner, "thread/inject_items")
	raw, _ := json.Marshal(inject["items"])
	want := `[{"content":[{"text":"fix the bug","type":"input_text"}],"role":"user","type":"message"},{"content":[{"text":"fixed","type":"output_text"}],"role":"assistant","type":"message"}]`
	if string(raw) != want {
		t.Fatalf("carried items = %s\nwant %s", raw, want)
	}
	var info bool
	for _, e := range events {
		if e.Kind == agent.KindSessionInfo && strings.Contains(e.Content, "run location changed") {
			info = true
		}
	}
	if !info {
		t.Fatalf("no relocation notice in %#v", events)
	}
}

func TestMatchingThreadIsResumed(t *testing.T) {
	stubCatalog(t)
	home := t.TempDir()
	writeRollout(t, home, "dev-thread", `{"type":"session_meta","payload":{"id":"dev-thread","dynamic_tools":[{"name":"Bash"}]}}`)
	spawner := &scriptedSpawner{}
	var calls []recordedCall
	ch := make(chan agent.StreamEvent, 32)
	if err := NewBackend().RunWithSession(context.Background(), "next", "/workspace", agent.RunRequest{
		ConfigDir: home, Spawner: spawner, SessionID: "dev-thread", IsResume: true, ToolRoute: deviceRoute(&calls),
	}, ch); err != nil {
		t.Fatal(err)
	}
	drain(ch)
	if !containsMethod(spawner.requests, "thread/resume") || containsMethod(spawner.requests, "thread/inject_items") {
		t.Fatalf("requests = %#v", spawner.requests)
	}

	// The same thread on a server-side turn carries tools it can't serve.
	plain := &scriptedSpawner{}
	ch = make(chan agent.StreamEvent, 32)
	if err := NewBackend().RunWithSession(context.Background(), "next", "/workspace", agent.RunRequest{
		ConfigDir: home, Spawner: plain, SessionID: "dev-thread", IsResume: true,
	}, ch); err != nil {
		t.Fatal(err)
	}
	drain(ch)
	if containsMethod(plain.requests, "thread/resume") {
		t.Fatal("moving back to the server must start a thread without device tools")
	}
	if _, ok := paramsOf(t, plain, "thread/start")["dynamicTools"]; ok {
		t.Fatal("server-side thread must not register device tools")
	}
}

func TestCarriedHistoryRestartsAtACompactionAndKeepsTheNewest(t *testing.T) {
	home := t.TempDir()
	big := strings.Repeat("x", carriedHistoryLimit/2+1)
	path := writeRollout(t, home, "t",
		`{"type":"session_meta","payload":{}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"before compaction"}]}}`,
		`{"type":"compacted","payload":{"message":"","replacement_history":[{"type":"message","role":"user","content":[{"type":"input_text","text":"summary"}]}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"`+big+`"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"`+big+`"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>srv</environment_context>"}]}}`,
	)
	items := carriedHistory(path)
	if len(items) != 1 || items[0]["role"] != "assistant" {
		t.Fatalf("items = %d %v; want only the newest message within the limit", len(items), items)
	}
}

func TestMCPResultBecomesDynamicToolContent(t *testing.T) {
	got := mcpToDynamicResult(json.RawMessage(`{"content":[{"type":"text","text":"boom"},{"type":"image","data":"QUJD","mimeType":"image/png"}],"isError":true}`))
	raw, _ := json.Marshal(got)
	want := `{"contentItems":[{"text":"boom","type":"inputText"},{"imageUrl":"data:image/png;base64,QUJD","type":"inputImage"}],"success":false}`
	if string(raw) != want {
		t.Fatalf("got %s\nwant %s", raw, want)
	}
	if r := toolCallResponse(context.Background(), nil, json.RawMessage(`{"tool":"Bash"}`)); r["success"] != false {
		t.Fatalf("a server-side turn must refuse a stale device tool: %v", r)
	}
}
