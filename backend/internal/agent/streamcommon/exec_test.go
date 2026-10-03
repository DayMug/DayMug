package streamcommon

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

func TestSetEnv(t *testing.T) {
	tests := []struct {
		name       string
		env        []string
		key, value string
		want       []string
	}{
		{
			name: "replaces existing key in place",
			env:  []string{"A=1", "B=2"},
			key:  "A", value: "9",
			want: []string{"A=9", "B=2"},
		},
		{
			name: "appends missing key",
			env:  []string{"A=1"},
			key:  "C", value: "3",
			want: []string{"A=1", "C=3"},
		},
		{
			name: "prefix of another key is not a match",
			env:  []string{"ABC=1"},
			key:  "AB", value: "2",
			want: []string{"ABC=1", "AB=2"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SetEnv(tc.env, tc.key, tc.value)
			if len(got) != len(tc.want) {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("entry %d: want %q, got %q", i, tc.want[i], got[i])
				}
			}
		})
	}
}

func drainOneshotWith(events []agent.StreamEvent, runErr error) (string, error) {
	ch := make(chan agent.StreamEvent, len(events)+1)
	for _, evt := range events {
		ch <- evt
	}
	close(ch)
	errCh := make(chan error, 1)
	errCh <- runErr
	return DrainOneshot(ch, errCh)
}

func TestDrainOneshot(t *testing.T) {
	tests := []struct {
		name    string
		events  []agent.StreamEvent
		runErr  error
		want    string
		wantErr bool
	}{
		{
			name: "prefers result over deltas",
			events: []agent.StreamEvent{
				{Kind: agent.KindDelta, Content: "partial"},
				{Kind: agent.KindResult, Content: " final "},
			},
			want: "final",
		},
		{
			name: "falls back to concatenated deltas",
			events: []agent.StreamEvent{
				{Kind: agent.KindDelta, Content: "a"},
				{Kind: agent.KindDelta, Content: "b"},
			},
			want: "ab",
		},
		{
			name: "skips subagent frames",
			events: []agent.StreamEvent{
				{Kind: agent.KindResult, Content: "worker", Subagent: true},
				{Kind: agent.KindDelta, Content: "parent"},
			},
			want: "parent",
		},
		{
			name:    "run error wins over drained text",
			events:  []agent.StreamEvent{{Kind: agent.KindResult, Content: "text"}},
			runErr:  errors.New("boom"),
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := drainOneshotWith(tc.events, tc.runErr)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("DrainOneshot: %v", err)
			}
			if got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestDrainStderr_CopiesAllBytes(t *testing.T) {
	buf, wait := DrainStderr(strings.NewReader("diagnostic blob"))
	wait()
	if buf.String() != "diagnostic blob" {
		t.Fatalf("want %q, got %q", "diagnostic blob", buf.String())
	}
}

func TestDrainStderr_BelowLimitIsByteIdentical(t *testing.T) {
	// The overwhelmingly common case must be untouched: no marker, no
	// reordering, no truncation.
	want := strings.Repeat("boring diagnostic line\n", 200)
	buf, wait := DrainStderr(strings.NewReader(want))
	wait()
	if buf.String() != want {
		t.Fatalf("stderr under the cap was altered: got %d bytes, want %d", buf.Len(), len(want))
	}
}

func TestDrainStderr_ExactlyHeadLimitHasNoMarker(t *testing.T) {
	want := strings.Repeat("x", stderrHeadLimit)
	buf, wait := DrainStderr(strings.NewReader(want))
	wait()
	if buf.String() != want {
		t.Fatalf("boundary case altered: got %d bytes, want %d", buf.Len(), len(want))
	}
}

func TestDrainStderr_JustOverHeadLimitStaysContiguous(t *testing.T) {
	// Nothing was discarded yet, so head and tail still join seamlessly and
	// there must be no marker claiming a gap.
	want := strings.Repeat("y", stderrHeadLimit+stderrTailChunk)
	buf, wait := DrainStderr(strings.NewReader(want))
	wait()
	if buf.String() != want {
		t.Fatalf("no bytes were dropped yet but the buffer changed: got %d bytes, want %d", buf.Len(), len(want))
	}
}

func TestDrainStderr_RunawayStderrIsCappedKeepingHeadAndTail(t *testing.T) {
	head := "FATAL: could not open config\n"
	tail := "\nlast gasp before exit\n"
	spam := strings.Repeat("noise noise noise\n", 500_000) // ~8.5 MB
	full := head + spam + tail

	buf, wait := DrainStderr(strings.NewReader(full))
	wait()
	got := buf.String()

	if ceiling := stderrHeadLimit + 2*stderrTailChunk + 128; len(got) > ceiling {
		t.Fatalf("retained %d bytes, want at most %d", len(got), ceiling)
	}
	if !strings.HasPrefix(got, head) {
		t.Errorf("first fatal line was dropped; buffer starts with %q", got[:min(len(got), 60)])
	}
	if !strings.HasSuffix(got, tail) {
		t.Errorf("the child's last words were dropped; buffer ends with %q", got[max(0, len(got)-60):])
	}
	if !strings.Contains(got, "bytes of stderr dropped") {
		t.Error("truncation is silent: no marker telling the reader bytes are missing")
	}
}

func TestDrainStderr_KeepsReadingAfterTheCapIsHit(t *testing.T) {
	// Regression guard for the reason DrainStderr exists at all: capping
	// retention must not stop consuming the pipe, or the child blocks on a full
	// kernel buffer instead of exiting.
	pr, pw := io.Pipe()
	buf, wait := DrainStderr(pr)

	chunk := strings.Repeat("z", 64<<10)
	written := make(chan error, 1)
	go func() {
		for range 32 { // 2 MB, far past the retention cap
			if _, err := pw.Write([]byte(chunk)); err != nil {
				written <- err
				return
			}
		}
		written <- pw.Close()
	}()

	if err := <-written; err != nil {
		t.Fatalf("writes blocked or failed after the cap was reached: %v", err)
	}
	wait()
	if buf.Len() == 0 {
		t.Error("expected the retained head to survive")
	}
}

func TestDrainStderr_NilReaderIsNoop(t *testing.T) {
	buf, wait := DrainStderr(nil)
	wait() // must not block or panic
	if buf.String() != "" {
		t.Fatalf("expected empty buffer, got %q", buf.String())
	}
}

func TestRunEnv(t *testing.T) {
	t.Setenv("DAYMUG_TEST_BASE", "from-server")
	t.Setenv("TERM", "xterm-256color")

	// The config-dir variable is the only per-CLI difference, so both names
	// must land verbatim from the same shared composition.
	claudeEnv := RunEnv(agent.RunRequest{ConfigDir: "/acct/claude"}, nil, "CLAUDE_CONFIG_DIR")
	if !slices.Contains(claudeEnv, "CLAUDE_CONFIG_DIR=/acct/claude") {
		t.Errorf("claude env missing CLAUDE_CONFIG_DIR: %v", claudeEnv)
	}
	codexEnv := RunEnv(agent.RunRequest{ConfigDir: "/acct/codex"}, nil, "CODEX_HOME")
	if !slices.Contains(codexEnv, "CODEX_HOME=/acct/codex") {
		t.Errorf("codex env missing CODEX_HOME: %v", codexEnv)
	}

	if !slices.Contains(claudeEnv, "DAYMUG_TEST_BASE=from-server") {
		t.Error("server process env should carry through")
	}
	// Pipe mode must not force a dumb terminal; only the PTY spawner needs it.
	for _, kv := range claudeEnv {
		if strings.HasPrefix(kv, "TERM=") && kv == "TERM=dumb" {
			t.Error("pipe mode should not set TERM=dumb")
		}
	}

	withExtras := RunEnv(agent.RunRequest{
		AccountEnv: map[string]string{"ANTHROPIC_API_KEY": "acct"},
		ConfigDir:  "/acct/claude",
		Spawner:    &agent.PtySpawner{},
	}, []string{"SANDBOX=1"}, "CLAUDE_CONFIG_DIR")
	for _, want := range []string{"ANTHROPIC_API_KEY=acct", "SANDBOX=1", "NO_COLOR=1", "TERM=dumb"} {
		if !slices.Contains(withExtras, want) {
			t.Errorf("env missing %q: %v", want, withExtras)
		}
	}
}
