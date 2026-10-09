package streamcommon

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

func TestEvent_MarshalsPayloadIntoSingleEvent(t *testing.T) {
	events := Event(agent.KindSystemInit, map[string]any{"model": "claude-opus-4-8"})
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Kind != agent.KindSystemInit {
		t.Errorf("kind = %q, want %q", events[0].Kind, agent.KindSystemInit)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(events[0].Content), &payload); err != nil {
		t.Fatalf("content is not valid JSON: %v", err)
	}
	if payload["model"] != "claude-opus-4-8" {
		t.Errorf("model = %q, want claude-opus-4-8", payload["model"])
	}
}

func TestEvent_DropsUnmarshalablePayload(t *testing.T) {
	// NaN is not representable in JSON; the frame must be dropped rather
	// than emitted with broken content.
	if events := Event(agent.KindUsage, map[string]any{"bad": math.NaN()}); events != nil {
		t.Fatalf("expected nil for unmarshalable payload, got %v", events)
	}
}

func TestWithJSONContent_ReplacesContent(t *testing.T) {
	evt := agent.StreamEvent{Kind: agent.KindUsage, Content: `{"old":1}`}
	got := WithJSONContent(evt, map[string]int{"new": 2})
	if got.Content != `{"new":2}` {
		t.Errorf("content = %q, want {\"new\":2}", got.Content)
	}
	if got.Kind != agent.KindUsage {
		t.Errorf("kind changed: %q", got.Kind)
	}
}

func TestWithJSONContent_KeepsOriginalOnMarshalError(t *testing.T) {
	evt := agent.StreamEvent{Kind: agent.KindUsage, Content: `{"old":1}`}
	got := WithJSONContent(evt, map[string]any{"bad": math.NaN()})
	if got.Content != `{"old":1}` {
		t.Errorf("content = %q, want original payload preserved", got.Content)
	}
}
