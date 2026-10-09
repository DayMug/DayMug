package codexcli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

func TestCodexStreamProcessor_TopLevelUsageDerivesDeltaFromResumeBaseline(t *testing.T) {
	p := NewStreamProcessor("gpt-5.6-sol")
	p.resumeUsage(&codexTokenUsage{
		InputTokens:         1000,
		CachedInputTokens:   800,
		OutputTokens:        100,
		ReasoningOutputToks: 20,
	})

	events := p.Process([]byte(`{"type":"turn.completed","usage":{"input_tokens":1300,"cached_input_tokens":1050,"output_tokens":140,"reasoning_output_tokens":30}}`))
	in, cached, out, reasoning := usageFromKindUsage(t, events)
	if in != 50 || cached != 250 || out != 40 || reasoning != 10 {
		t.Fatalf("resumed turn got input=%d cached=%d output=%d reasoning=%d, want 50/250/40/10", in, cached, out, reasoning)
	}
}

func TestCodexStreamProcessor_DropsResumeUsageWithoutBaseline(t *testing.T) {
	p := NewStreamProcessor("gpt-5.6-sol")
	p.resumeUsage(nil)

	first := p.Process([]byte(`{"type":"turn.completed","usage":{"input_tokens":1300,"cached_input_tokens":1050,"output_tokens":140}}`))
	if len(first) != 0 {
		t.Fatalf("cumulative usage without a resume baseline must be dropped, got %+v", first)
	}

	// Once a cumulative frame has established a safe in-process baseline,
	// subsequent frames can be differenced normally.
	second := p.Process([]byte(`{"type":"turn.completed","usage":{"input_tokens":1500,"cached_input_tokens":1200,"output_tokens":160}}`))
	in, cached, out, _ := usageFromKindUsage(t, second)
	if in != 50 || cached != 150 || out != 20 {
		t.Fatalf("next turn got input=%d cached=%d output=%d, want 50/150/20", in, cached, out)
	}
}

func TestReadLatestCodexTotalUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := "" +
		`{"timestamp":"2026-08-12T08:00:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":80,"output_tokens":10}}}}` + "\n" +
		`{"timestamp":"2026-08-12T08:01:00Z","type":"event_msg","payload":{"type":"agent_message","message":"done"}}` + "\n" +
		`{"timestamp":"2026-08-12T08:02:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":250,"cached_input_tokens":210,"output_tokens":25,"reasoning_output_tokens":4}}}}` + "\n" +
		`{"timestamp":"truncated"`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	got, err := readLatestCodexTotalUsage(path)
	if err != nil {
		t.Fatalf("readLatestCodexTotalUsage: %v", err)
	}
	want := codexTokenUsage{InputTokens: 250, CachedInputTokens: 210, OutputTokens: 25, ReasoningOutputToks: 4}
	if got == nil || *got != want {
		t.Fatalf("latest usage = %+v, want %+v", got, want)
	}
}

func TestCodexRunner_ResumeSeedsTopLevelUsageFromRollout(t *testing.T) {
	codexHome := t.TempDir()
	sessionID := "019ff4ec-454d-7502-a74b-929f7a291716"
	dayDir := filepath.Join(codexHome, "sessions", "2026", "08", "12")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatalf("mkdir rollout tree: %v", err)
	}
	rollout := filepath.Join(dayDir, "rollout-2026-08-12T07-42-32-"+sessionID+".jsonl")
	previous := `{"timestamp":"2026-08-12T08:00:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1000,"cached_input_tokens":800,"output_tokens":100,"reasoning_output_tokens":20}}}}` + "\n"
	if err := os.WriteFile(rollout, []byte(previous), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	spawner := &codexCaptureSpawner{stdout: `{"type":"turn.completed","usage":{"input_tokens":1300,"cached_input_tokens":1050,"output_tokens":140,"reasoning_output_tokens":30}}` + "\n"}
	ch := make(chan agent.StreamEvent, 8)
	opts := agent.RunRequest{
		SessionID: sessionID,
		IsResume:  true,
		Model:     "gpt-5.6-sol",
		ConfigDir: codexHome,
		Spawner:   spawner,
	}
	if err := NewBackend().RunWithSession(context.Background(), "prompt", t.TempDir(), opts, ch); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}

	var events []agent.StreamEvent
	for event := range ch {
		events = append(events, event)
	}
	in, cached, out, reasoning := usageFromKindUsage(t, events)
	if in != 50 || cached != 250 || out != 40 || reasoning != 10 {
		t.Fatalf("persisted turn got input=%d cached=%d output=%d reasoning=%d, want 50/250/40/10", in, cached, out, reasoning)
	}
}
