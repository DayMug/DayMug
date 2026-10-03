package service

import (
	"encoding/json"
	"strings"
	"testing"
)

// The queue counts are pointers so that "nothing is ahead of you" reaches the
// client. When they were plain ints, omitempty erased a genuine 0 and the
// frontend fell back to the 1-based position, telling the user one task was
// ahead when none was.
func TestServerMessage_QueueCountsSurviveZero(t *testing.T) {
	zero := 0
	data, err := json.Marshal(ServerMessage{
		Type:          "queue_status",
		Status:        "queued",
		QueuePosition: 1,
		QueueAhead:    &zero,
		QueueRunning:  &zero,
	})
	if err != nil {
		t.Fatal(err)
	}

	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"queue_ahead", "queue_running"} {
		got, ok := wire[key]
		if !ok {
			t.Errorf("%s missing from the wire; a real zero must not be erased", key)
			continue
		}
		if got != float64(0) {
			t.Errorf("%s = %v, want 0", key, got)
		}
	}
}

// Every other event type shares this struct, so leaving the counts nil has to
// keep them off the wire entirely — that is what earns them omitempty.
func TestServerMessage_QueueCountsAbsentWhenUnset(t *testing.T) {
	data, err := json.Marshal(ServerMessage{Type: "delta", Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"queue_ahead", "queue_running"} {
		if strings.Contains(string(data), key) {
			t.Errorf("%s leaked onto a %q frame: %s", key, "delta", data)
		}
	}
}
