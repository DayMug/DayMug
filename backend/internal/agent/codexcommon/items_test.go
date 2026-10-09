package codexcommon

import (
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

func TestItemEvents(t *testing.T) {
	cases := []struct {
		name     string
		item     Item
		started  bool
		wantKind string // "" = no event
		contains string
	}{
		{"unknown kind", Item{Kind: ItemUnknown, ID: "x"}, false, "", ""},
		{"agent message started is silent", Item{Kind: ItemAgentMessage, Text: "hi"}, true, "", ""},
		{"agent message completed", Item{Kind: ItemAgentMessage, Text: "hi"}, false, agent.KindResult, "hi"},
		{"empty agent message", Item{Kind: ItemAgentMessage}, false, "", ""},
		{"reasoning completed", Item{Kind: ItemReasoning, Text: "think"}, false, agent.KindThinkingDelta, "think"},
		{"reasoning started is silent", Item{Kind: ItemReasoning, Text: "think"}, true, "", ""},
		{"mcp tool only", Item{Kind: ItemMcpToolCall, ID: "m", Tool: "search"}, true, agent.KindToolUseStart, `"name":"search"`},
		{"mcp nameless", Item{Kind: ItemMcpToolCall, ID: "m"}, true, agent.KindToolUseStart, `"name":"McpToolCall"`},
		{"mcp empty result falls back to status", Item{Kind: ItemMcpToolCall, ID: "m", Status: "completed"}, false, agent.KindToolResult, `"content":"(completed)"`},
		{"command started", Item{Kind: ItemCommand, ID: "c", Command: "ls"}, true, agent.KindToolUseStart, `"name":"Bash"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := ItemEvents(tc.item, tc.started)
			if tc.wantKind == "" {
				if len(events) != 0 {
					t.Fatalf("events = %#v, want none", events)
				}
				return
			}
			if len(events) != 1 || events[0].Kind != tc.wantKind {
				t.Fatalf("events = %#v, want one %s", events, tc.wantKind)
			}
			if !strings.Contains(events[0].Content, tc.contains) {
				t.Fatalf("content %q missing %q", events[0].Content, tc.contains)
			}
		})
	}
}
