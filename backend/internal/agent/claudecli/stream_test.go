package claudecli

import (
	"encoding/json"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

func TestParseStreamLine_BackgroundTasksChangedCarriesTheWholeSet(t *testing.T) {
	line := `{"type":"system","subtype":"background_tasks_changed","tasks":[` +
		`{"task_id":"b2r4fq0um","task_type":"monitor","description":"e2e run progress"},` +
		`{"task_id":"x7","task_type":"bash","description":"pnpm build","ambient":true}` +
		`],"uuid":"u","session_id":"s"}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Kind != agent.KindBackgroundTasks {
		t.Fatalf("expected kind=%s, got %q", agent.KindBackgroundTasks, events[0].Kind)
	}
	var got struct {
		Tasks []struct {
			TaskID      string `json:"task_id"`
			TaskType    string `json:"task_type"`
			Description string `json:"description"`
			Ambient     bool   `json:"ambient"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(events[0].Content), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %+v", got.Tasks)
	}
	if got.Tasks[0].TaskID != "b2r4fq0um" || got.Tasks[0].TaskType != "monitor" {
		t.Errorf("first task lost its identity: %+v", got.Tasks[0])
	}
	if got.Tasks[1].Description != "pnpm build" {
		t.Errorf("second task lost its description: %+v", got.Tasks[1])
	}
	if !got.Tasks[1].Ambient {
		t.Errorf("second task lost its ambient marker: %+v", got.Tasks[1])
	}
}

// The empty set is the whole point of the level signal — it is what tells a
// resident bridge the work it stayed alive for has finished. Dropping it as
// "nothing to report" would wedge the bridge alive forever.
func TestParseStreamLine_BackgroundTasksChangedEmitsTheEmptySet(t *testing.T) {
	for name, line := range map[string]string{
		"explicit empty array": `{"type":"system","subtype":"background_tasks_changed","tasks":[],"uuid":"u","session_id":"s"}`,
		"absent tasks key":     `{"type":"system","subtype":"background_tasks_changed","uuid":"u","session_id":"s"}`,
	} {
		t.Run(name, func(t *testing.T) {
			events := ParseStreamLine([]byte(line))
			if len(events) != 1 {
				t.Fatalf("expected 1 event, got %d", len(events))
			}
			if events[0].Kind != agent.KindBackgroundTasks {
				t.Fatalf("expected kind=%s, got %q", agent.KindBackgroundTasks, events[0].Kind)
			}
			var got struct {
				Tasks []json.RawMessage `json:"tasks"`
			}
			if err := json.Unmarshal([]byte(events[0].Content), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(got.Tasks) != 0 {
				t.Fatalf("expected an empty task set, got %v", got.Tasks)
			}
			// The key itself must survive: a consumer that swaps its set for
			// payload.tasks would otherwise keep its stale set on a null.
			if !json.Valid([]byte(events[0].Content)) {
				t.Fatalf("payload is not valid JSON: %q", events[0].Content)
			}
		})
	}
}

func TestParseStreamLine_TaskNotification(t *testing.T) {
	line := `{"type":"system","subtype":"task_notification","task_id":"b2r4fq0um",` +
		`"status":"completed","output_file":"/tmp/x.log","summary":"Monitor: codex leg finished",` +
		`"uuid":"u","session_id":"s"}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Kind != agent.KindTaskNotification {
		t.Fatalf("expected kind=%s, got %q", agent.KindTaskNotification, events[0].Kind)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(events[0].Content), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["summary"] != "Monitor: codex leg finished" {
		t.Errorf("summary lost: %v", got["summary"])
	}
	if got["status"] != "completed" {
		t.Errorf("status lost: %v", got["status"])
	}
	if got["task_id"] != "b2r4fq0um" {
		t.Errorf("task_id lost: %v", got["task_id"])
	}
}

func TestParseStreamLine_TaskNotificationWithoutSummaryIsDropped(t *testing.T) {
	line := `{"type":"system","subtype":"task_notification","task_id":"b2","status":"stopped","uuid":"u","session_id":"s"}`
	if events := ParseStreamLine([]byte(line)); len(events) != 0 {
		t.Fatalf("expected a summary-less notification to be dropped, got %+v", events)
	}
}

// Guards the switch: the CLI emits many more system subtypes than we map, and
// silently ignoring them is deliberate. A `default` that started emitting
// would flood every consumer with frames nothing renders.
func TestParseStreamLine_UnmappedSystemSubtypesEmitNothing(t *testing.T) {
	for _, subtype := range []string{
		"thinking_tokens", "session_state_changed", "task_started",
		"task_updated", "task_progress", "worker_shutting_down", "",
	} {
		line := `{"type":"system","subtype":"` + subtype + `","uuid":"u","session_id":"s"}`
		if events := ParseStreamLine([]byte(line)); len(events) != 0 {
			t.Errorf("subtype %q should emit nothing, got %+v", subtype, events)
		}
	}
}

// The level set belongs to the process, and a sub-agent has no process of its
// own — but the frame still carries parent_tool_use_id when a worker spawns
// the task, so the flag has to survive for a consumer to filter on.
func TestParseStreamLine_BackgroundTasksKeepsSubagentFlag(t *testing.T) {
	line := `{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"t"}],` +
		`"parent_tool_use_id":"toolu_1","uuid":"u","session_id":"s"}`
	events := ParseStreamLine([]byte(line))
	if len(events) != 1 || !events[0].Subagent {
		t.Fatalf("expected one sub-agent-flagged event, got %+v", events)
	}
}

// StreamProcessor has a per-kind switch; the new kinds must fall through it
// untouched rather than be absorbed the way KindLiveUsage is.
func TestStreamProcessor_PassesBackgroundFramesThrough(t *testing.T) {
	p := NewStreamProcessor()
	tasks := p.Process([]byte(`{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"t"}]}`))
	if len(tasks) != 1 || tasks[0].Kind != agent.KindBackgroundTasks {
		t.Fatalf("background_tasks_changed did not survive Process: %+v", tasks)
	}
	notify := p.Process([]byte(`{"type":"system","subtype":"task_notification","task_id":"t","status":"completed","summary":"done"}`))
	if len(notify) != 1 || notify[0].Kind != agent.KindTaskNotification {
		t.Fatalf("task_notification did not survive Process: %+v", notify)
	}
}
