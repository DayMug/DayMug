package codexcli

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/codexcommon"
)

// TestExecToolItems covers the side-effect items `codex exec --json` emits
// besides command_execution. Wire shapes follow codex-rs exec_events.rs
// (snake_case type tags; file_change carries changes[{path,kind}] + status,
// mcp_tool_call carries server/tool/arguments/result/error/status, web_search
// carries query). Each row asserts two things: the exec wire decodes into the
// same normalized item the app-server transport builds, and the resulting tool
// card has the name and payload the UI renders.
func TestExecToolItems(t *testing.T) {
	cases := []struct {
		name        string
		line        string
		started     bool
		want        codexcommon.Item
		wantKind    string
		wantPayload map[string]any
	}{
		{
			name:    "file_change started",
			line:    `{"type":"item.started","item":{"id":"p1","type":"file_change","status":"in_progress","changes":[{"path":"/w/a.go","kind":"update"},{"path":"/w/b.go","kind":"add"}]}}`,
			started: true,
			want: codexcommon.Item{Kind: codexcommon.ItemFileChange, ID: "p1", Status: "in_progress",
				Changes: []codexcommon.FileChange{{Path: "/w/a.go", Kind: "update"}, {Path: "/w/b.go", Kind: "add"}}},
			wantKind: agent.KindToolUseStart,
			wantPayload: map[string]any{"id": "p1", "name": "ApplyPatch",
				"input": map[string]any{"files": []any{"/w/a.go", "/w/b.go"}}},
		},
		{
			name: "file_change completed",
			line: `{"type":"item.completed","item":{"id":"p1","type":"file_change","status":"completed","changes":[{"path":"/w/a.go","kind":"update"},{"path":"/w/old.go","kind":"delete"}]}}`,
			want: codexcommon.Item{Kind: codexcommon.ItemFileChange, ID: "p1", Status: "completed",
				Changes: []codexcommon.FileChange{{Path: "/w/a.go", Kind: "update"}, {Path: "/w/old.go", Kind: "delete"}}},
			wantKind: agent.KindToolResult,
			wantPayload: map[string]any{"id": "p1", "name": "ApplyPatch", "content": "update /w/a.go\ndelete /w/old.go",
				"input": map[string]any{"files": []any{"/w/a.go", "/w/old.go"}, "status": "completed"}},
		},
		{
			name:     "file_change failed without changes",
			line:     `{"type":"item.completed","item":{"id":"p2","type":"file_change","status":"failed","changes":[]}}`,
			want:     codexcommon.Item{Kind: codexcommon.ItemFileChange, ID: "p2", Status: "failed"},
			wantKind: agent.KindToolResult,
			wantPayload: map[string]any{"id": "p2", "name": "ApplyPatch", "content": "(failed)",
				"input": map[string]any{"files": []any{}, "status": "failed"}},
		},
		{
			name:    "mcp_tool_call started",
			line:    `{"type":"item.started","item":{"id":"m1","type":"mcp_tool_call","server":"notion","tool":"search","arguments":{"q":"x"},"status":"in_progress"}}`,
			started: true,
			want: codexcommon.Item{Kind: codexcommon.ItemMcpToolCall, ID: "m1", Status: "in_progress",
				Server: "notion", Tool: "search", Arguments: json.RawMessage(`{"q":"x"}`)},
			wantKind: agent.KindToolUseStart,
			wantPayload: map[string]any{"id": "m1", "name": "mcp__notion__search",
				"input": map[string]any{"server": "notion", "tool": "search", "arguments": map[string]any{"q": "x"}}},
		},
		{
			name: "mcp_tool_call completed",
			line: `{"type":"item.completed","item":{"id":"m1","type":"mcp_tool_call","server":"notion","tool":"search","arguments":{"q":"x"},"result":{"content":[{"type":"text","text":"hit"}],"structured_content":null},"status":"completed"}}`,
			want: codexcommon.Item{Kind: codexcommon.ItemMcpToolCall, ID: "m1", Status: "completed",
				Server: "notion", Tool: "search", Arguments: json.RawMessage(`{"q":"x"}`),
				Result: json.RawMessage(`{"content":[{"type":"text","text":"hit"}],"structured_content":null}`)},
			wantKind: agent.KindToolResult,
			wantPayload: map[string]any{"id": "m1", "name": "mcp__notion__search",
				"content": `{"content":[{"type":"text","text":"hit"}],"structured_content":null}`,
				"input":   map[string]any{"server": "notion", "tool": "search", "arguments": map[string]any{"q": "x"}}},
		},
		{
			name: "mcp_tool_call failed",
			line: `{"type":"item.completed","item":{"id":"m2","type":"mcp_tool_call","server":"notion","tool":"search","arguments":null,"error":{"message":"server unavailable"},"status":"failed"}}`,
			want: codexcommon.Item{Kind: codexcommon.ItemMcpToolCall, ID: "m2", Status: "failed",
				Server: "notion", Tool: "search", Arguments: json.RawMessage(`null`), ErrorMessage: "server unavailable"},
			wantKind: agent.KindToolResult,
			wantPayload: map[string]any{"id": "m2", "name": "mcp__notion__search", "content": "server unavailable",
				"input": map[string]any{"server": "notion", "tool": "search"}},
		},
		{
			name:        "web_search started",
			line:        `{"type":"item.started","item":{"id":"w1","type":"web_search","query":"golang generics"}}`,
			started:     true,
			want:        codexcommon.Item{Kind: codexcommon.ItemWebSearch, ID: "w1", Query: "golang generics"},
			wantKind:    agent.KindToolUseStart,
			wantPayload: map[string]any{"id": "w1", "name": "WebSearch", "input": map[string]any{"query": "golang generics"}},
		},
		{
			name:     "web_search completed",
			line:     `{"type":"item.completed","item":{"id":"w1","type":"web_search","query":"golang generics","action":{"type":"search","query":"golang generics"}}}`,
			want:     codexcommon.Item{Kind: codexcommon.ItemWebSearch, ID: "w1", Query: "golang generics"},
			wantKind: agent.KindToolResult,
			wantPayload: map[string]any{"id": "w1", "name": "WebSearch", "content": "golang generics",
				"input": map[string]any{"query": "golang generics"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var env codexEnvelope
			if err := json.Unmarshal([]byte(tc.line), &env); err != nil || env.Item == nil {
				t.Fatalf("decode %s: %v", tc.line, err)
			}
			got := env.Item.common()
			if len(got.Changes) == 0 {
				got.Changes = nil
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("normalized item:\n got  %#v\n want %#v", got, tc.want)
			}

			events := ParseCodexLine([]byte(tc.line))
			if len(events) != 1 || events[0].Kind != tc.wantKind {
				t.Fatalf("events = %#v, want one %s", events, tc.wantKind)
			}
			if want := codexcommon.ItemEvents(tc.want, tc.started); !reflect.DeepEqual(events, want) {
				t.Fatalf("exec events diverge from the shared mapping:\n got  %#v\n want %#v", events, want)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(events[0].Content), &payload); err != nil {
				t.Fatalf("payload %q: %v", events[0].Content, err)
			}
			if !reflect.DeepEqual(payload, tc.wantPayload) {
				t.Fatalf("payload:\n got  %#v\n want %#v", payload, tc.wantPayload)
			}
		})
	}
}

func TestExecUnknownItemIsDropped(t *testing.T) {
	line := `{"type":"item.completed","item":{"id":"t1","type":"todo_list","items":[{"text":"x","completed":false}]}}`
	if events := ParseCodexLine([]byte(line)); len(events) != 0 {
		t.Fatalf("events = %#v, want none", events)
	}
}
