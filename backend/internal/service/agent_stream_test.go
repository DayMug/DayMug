package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// multiResultBackend emits several result frames in one turn — the codex shape
// that makes the per-result / aggregate distinction observable. Anything else
// (a single result frame) renders identically in both modes.
type multiResultBackend struct{ results []string }

func (multiResultBackend) Name() string                     { return "multi-result" }
func (multiResultBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b multiResultBackend) RunWithSession(_ context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	for _, r := range b.results {
		ch <- agent.StreamEvent{Kind: agent.KindResult, Content: r}
	}
	close(ch)
	return nil
}
func (multiResultBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (multiResultBackend) SessionExists(string, string, string) bool    { return false }
func (multiResultBackend) SessionLogPath(string, string, string) string { return "" }

// silentBackend produces the given deltas and then ends the turn with runErr,
// never emitting a result frame — the "agent said nothing" case the aggregate
// placeholder exists for.
type silentBackend struct {
	deltas []string
	runErr error
}

func (silentBackend) Name() string                     { return "silent" }
func (silentBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b silentBackend) RunWithSession(_ context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	for _, d := range b.deltas {
		ch <- agent.StreamEvent{Kind: agent.KindDelta, Content: d}
	}
	close(ch)
	return b.runErr
}
func (silentBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (silentBackend) SessionExists(string, string, string) bool    { return false }
func (silentBackend) SessionLogPath(string, string, string) string { return "" }

func assistantContents(t *testing.T, ms *storetest.Fake, convID string) []string {
	t.Helper()
	var out []string
	for _, msg := range ms.SnapshotMessages(convID) {
		if msg.Role == "assistant" {
			out = append(out, msg.Content)
		}
	}
	return out
}

func resultFrames(frames []ServerMessage) []string {
	var out []string
	for _, f := range frames {
		if f.Type != "result" {
			continue
		}
		text, _ := f.Content.(string)
		out = append(out, text)
	}
	return out
}

// TestAgentStreamerModeShapesResultRows pins the one legitimate difference
// between the two transports: the web transcript is append-only so every result
// frame becomes its own row, while an IM thread has a single delivery slot and
// must collapse the turn into one reply. Splitting an IM turn would post only
// the last fragment to the channel.
func TestAgentStreamerModeShapesResultRows(t *testing.T) {
	const convID = "conv"
	backend := multiResultBackend{results: []string{"第一段", "第二段", "第三段"}}

	cases := []struct {
		name        string
		mode        AgentStreamMode
		wantContent string
		wantRows    []string
		wantFrames  []string
	}{
		{
			name:        "per-result keeps one row per frame",
			mode:        AgentStreamPerResult,
			wantContent: "第三段",
			wantRows:    []string{"第一段", "第二段", "第三段"},
			wantFrames:  []string{"第一段", "第二段", "第三段"},
		},
		{
			// One row for the turn, but the per-step frames stay on their own
			// lines: this join used to be raw concatenation, which welded each
			// step's last line onto the next step's first one.
			name:        "aggregate collapses the turn into one row",
			mode:        AgentStreamAggregate,
			wantContent: "第一段\n第二段\n第三段",
			wantRows:    []string{"第一段\n第二段\n第三段"},
			wantFrames:  []string{"第一段\n第二段\n第三段"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ms := storetest.New()
			var frames []ServerMessage
			streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster()}
			out := streamer.Run(context.Background(), AgentStreamRequest{
				Backend:        backend,
				WorkDir:        t.TempDir(),
				ConversationID: convID,
				Mode:           tc.mode,
				Broadcast:      func(msg ServerMessage) { frames = append(frames, msg) },
			})
			if out.Err != nil {
				t.Fatalf("run error = %v", out.Err)
			}
			if out.Content != tc.wantContent {
				t.Fatalf("content = %q, want %q", out.Content, tc.wantContent)
			}
			if got := assistantContents(t, ms, convID); !equalStrings(got, tc.wantRows) {
				t.Fatalf("assistant rows = %v, want %v", got, tc.wantRows)
			}
			if got := resultFrames(frames); !equalStrings(got, tc.wantFrames) {
				t.Fatalf("result frames = %v, want %v", got, tc.wantFrames)
			}
		})
	}
}

// Agent output is persisted and broadcast byte-for-byte. Absolute paths are
// useful operational context and must not be shortened or replaced at this
// transport boundary.
func TestAgentStreamerPreservesAbsolutePaths(t *testing.T) {
	const convID = "conv"
	workDir := t.TempDir()
	want := "wrote " + filepath.Join(workDir, "out.txt") + "; used /usr/bin/git"
	ms := storetest.New()
	var frames []ServerMessage
	streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster()}

	out := streamer.Run(context.Background(), AgentStreamRequest{
		Backend:        multiResultBackend{results: []string{want}},
		WorkDir:        workDir,
		ConversationID: convID,
		Mode:           AgentStreamPerResult,
		Broadcast:      func(msg ServerMessage) { frames = append(frames, msg) },
	})

	if out.Err != nil {
		t.Fatalf("run error = %v", out.Err)
	}
	if rows := assistantContents(t, ms, convID); len(rows) != 1 || rows[0] != want {
		t.Fatalf("persisted rows = %v, want [%q]", rows, want)
	}
	if got := resultFrames(frames); len(got) != 1 || got[0] != want {
		t.Fatalf("broadcast frames = %v, want [%q]", got, want)
	}
}

// TestAgentStreamerEmptyResultText pins that the placeholder stands in only for
// a *successful* silent turn. A failed or cancelled run must report its partial
// output (or nothing) rather than claim the agent answered with the
// placeholder, and the web transcript — which passes no placeholder — must keep
// persisting nothing for a silent turn.
func TestAgentStreamerEmptyResultText(t *testing.T) {
	const (
		convID      = "conv"
		placeholder = "(agent 没有返回内容)"
	)
	boom := errors.New("backend exploded")

	cases := []struct {
		name        string
		mode        AgentStreamMode
		empty       string
		backend     silentBackend
		wantContent string
		wantRows    []string
	}{
		{
			name:        "aggregate silent success falls back to the placeholder",
			mode:        AgentStreamAggregate,
			empty:       placeholder,
			backend:     silentBackend{},
			wantContent: placeholder,
			wantRows:    []string{placeholder},
		},
		{
			name:        "aggregate failure keeps its partial output",
			mode:        AgentStreamAggregate,
			empty:       placeholder,
			backend:     silentBackend{deltas: []string{"只写到一半"}, runErr: boom},
			wantContent: "只写到一半",
			wantRows:    []string{"只写到一半"},
		},
		{
			name:        "aggregate silent failure claims nothing",
			mode:        AgentStreamAggregate,
			empty:       placeholder,
			backend:     silentBackend{runErr: boom},
			wantContent: "",
			wantRows:    nil,
		},
		{
			name:        "per-result silent success persists nothing",
			mode:        AgentStreamPerResult,
			backend:     silentBackend{},
			wantContent: "",
			wantRows:    nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ms := storetest.New()
			streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster()}
			out := streamer.Run(context.Background(), AgentStreamRequest{
				Backend:         tc.backend,
				WorkDir:         t.TempDir(),
				ConversationID:  convID,
				Mode:            tc.mode,
				EmptyResultText: tc.empty,
				Broadcast:       func(ServerMessage) {},
			})
			if out.Content != tc.wantContent {
				t.Fatalf("content = %q, want %q", out.Content, tc.wantContent)
			}
			if got := assistantContents(t, ms, convID); !equalStrings(got, tc.wantRows) {
				t.Fatalf("assistant rows = %v, want %v", got, tc.wantRows)
			}
		})
	}
}

// firstTurnSessionBackend names its own session mid-stream (the codex shape)
// and starts a brand-new session log for it, then blocks until the run is
// cancelled.
type firstTurnSessionBackend struct {
	sessionID string
	logPath   string
	started   chan struct{}
}

func (*firstTurnSessionBackend) Name() string                     { return "first-turn-session" }
func (*firstTurnSessionBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b *firstTurnSessionBackend) RunWithSession(ctx context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	if err := os.WriteFile(b.logPath, []byte(`{"role":"user","text":"第一轮"}`+"\n"), 0o600); err != nil {
		close(ch)
		return err
	}
	ch <- agent.StreamEvent{Kind: agent.KindSystemInit, Content: `{"session_id":"` + b.sessionID + `"}`}
	b.started <- struct{}{}
	<-ctx.Done()
	close(ch)
	return errors.New("signal: terminated")
}
func (*firstTurnSessionBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (*firstTurnSessionBackend) SessionExists(string, string, string) bool { return false }
func (b *firstTurnSessionBackend) SessionLogPath(_, sessionID, _ string) string {
	if sessionID != b.sessionID {
		return ""
	}
	return b.logPath
}

// TestAgentStreamerPreservesFirstTurnSessionLogOnCancel verifies that a Stop
// interrupts execution without turning the next message into a fresh session.
func TestAgentStreamerPreservesFirstTurnSessionLogOnCancel(t *testing.T) {
	const (
		convID    = "conv"
		sessionID = "codex-session-1"
	)
	logPath := filepath.Join(t.TempDir(), "rollout.jsonl")
	ms := storetest.New()
	ms.Conversations = []store.Conversation{{ID: convID}}
	broadcaster := NewBroadcaster()
	backend := &firstTurnSessionBackend{sessionID: sessionID, logPath: logPath, started: make(chan struct{})}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !broadcaster.StartJob(convID, cancel) {
		t.Fatal("StartJob refused a fresh conversation")
	}
	defer broadcaster.EndJob(convID)

	streamer := &AgentStreamer{Store: ms, Broadcaster: broadcaster}
	done := make(chan AgentStreamResult, 1)
	go func() {
		done <- streamer.Run(ctx, AgentStreamRequest{
			Backend:        backend,
			WorkDir:        t.TempDir(),
			ConversationID: convID,
			Mode:           AgentStreamPerResult,
			Broadcast:      func(ServerMessage) {},
		})
	}()

	select {
	case <-backend.started:
	case <-time.After(2 * time.Second):
		t.Fatal("backend never started")
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("session log missing before cancel: %v", err)
	}
	if !broadcaster.CancelJob(convID) {
		t.Fatal("CancelJob found no registered job")
	}

	var out AgentStreamResult
	select {
	case out = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not finish after cancel")
	}
	if !out.Cancelled {
		t.Fatal("result did not report the run as cancelled")
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("first-turn session log missing after cancel: %v", err)
	}
	if want := "{\"role\":\"user\",\"text\":\"第一轮\"}\n"; string(got) != want {
		t.Fatalf("first-turn session log after cancel = %q, want %q", got, want)
	}
}

// frameScriptBackend replays a fixed event script, standing in for a CLI whose
// turn interleaves reasoning, narration and tool calls.
type frameScriptBackend struct{ events []agent.StreamEvent }

func (frameScriptBackend) Name() string                     { return "frame-script" }
func (frameScriptBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b frameScriptBackend) RunWithSession(_ context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	for _, e := range b.events {
		ch <- e
	}
	close(ch)
	return nil
}
func (frameScriptBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (frameScriptBackend) SessionExists(string, string, string) bool    { return false }
func (frameScriptBackend) SessionLogPath(string, string, string) string { return "" }

func TestAgentStreamerPersistsCASSessionWarningAndStreamsHealthyDiagnostics(t *testing.T) {
	const (
		convID  = "conv"
		warning = "CAS could not resume old-thread and started a fresh thread new-thread"
	)
	ms := storetest.New()
	var frames []ServerMessage
	streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster()}
	out := streamer.Run(context.Background(), AgentStreamRequest{
		Backend: frameScriptBackend{events: []agent.StreamEvent{
			{Kind: agent.KindSessionWarning, Content: warning},
			{Kind: agent.KindSessionInfo, Content: "CAS resumed thread new-thread"},
		}},
		WorkDir:        t.TempDir(),
		ConversationID: convID,
		Mode:           AgentStreamPerResult,
		Broadcast:      func(msg ServerMessage) { frames = append(frames, msg) },
	})
	if out.Err != nil {
		t.Fatalf("run error = %v", out.Err)
	}

	messages := ms.SnapshotMessages(convID)
	if len(messages) != 1 || messages[0].Role != "error" || messages[0].Content != warning {
		t.Fatalf("persisted messages = %+v, want one error warning", messages)
	}
	if string(messages[0].Metadata) != `{"notice_type":"session_warning"}` {
		t.Fatalf("warning metadata = %s", messages[0].Metadata)
	}
	if len(frames) != 2 {
		t.Fatalf("frames = %+v, want warning and info", frames)
	}
	if frames[0].Type != "session_warning" || frames[0].Message != warning || frames[0].MessageID != messages[0].ID {
		t.Fatalf("warning frame = %+v, want persisted warning id %q", frames[0], messages[0].ID)
	}
	if frames[1].Type != "session_info" || frames[1].Content != "CAS resumed thread new-thread" {
		t.Fatalf("info frame = %+v", frames[1])
	}
}

// joinLate subscribes a fresh client to the conversation and returns whatever
// the broadcaster replays to it — i.e. exactly what a second browser opening
// the conversation right now would be able to reconstruct.
func joinLate(t *testing.T, b *Broadcaster, convID string) []ServerMessage {
	t.Helper()
	ch := make(chan []byte, 32)
	b.JoinSubscription(convID, "late-"+convID, "sub", ch)
	var out []ServerMessage
	for {
		select {
		case data := <-ch:
			var msg ServerMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("replayed frame is not a ServerMessage: %v", err)
			}
			out = append(out, msg)
		default:
			return out
		}
	}
}

func frameSummary(frames []ServerMessage) []string {
	out := make([]string, 0, len(frames))
	for _, f := range frames {
		text, _ := f.Content.(string)
		out = append(out, f.Type+":"+text)
	}
	return out
}

// TestAgentStreamerReplaysInFlightTurnToLateJoiner pins the fix for two
// browsers showing different states of the same running conversation. Tool
// calls are persisted mid-turn and drop the replay buffer, but the reasoning
// and narration streamed alongside them are only persisted when the turn's
// result lands — so without a snapshot the tab that joins mid-turn can never
// obtain them, and in simple view mode (which hides tool rows) it shows an
// empty transcript while the tab that watched live shows the whole turn.
func TestAgentStreamerReplaysInFlightTurnToLateJoiner(t *testing.T) {
	const convID = "conv-inflight"
	ms := storetest.New()
	b := NewBroadcaster()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !b.StartJob(convID, cancel) {
		t.Fatal("StartJob refused a fresh conversation")
	}
	defer b.EndJob(convID)

	var replayed []ServerMessage
	streamer := &AgentStreamer{Store: ms, Broadcaster: b}
	streamer.Run(ctx, AgentStreamRequest{
		Backend: frameScriptBackend{events: []agent.StreamEvent{
			{Kind: agent.KindThinkingDelta, Content: "先看看构建脚本"},
			{Kind: agent.KindDelta, Content: "构建是干净的，接着看部署。"},
			{Kind: agent.KindToolResult, Content: `{"name":"Read","input":{}}`},
			{Kind: agent.KindDelta, Content: "读完了。"},
		}},
		WorkDir:        t.TempDir(),
		ConversationID: convID,
		Mode:           AgentStreamPerResult,
		// The frame after the tool call is the first moment the checkpoint has
		// been taken, which is where a second browser would join.
		Broadcast: func(msg ServerMessage) {
			if text, _ := msg.Content.(string); msg.Type == "delta" && text == "读完了。" {
				replayed = joinLate(t, b, convID)
			}
		},
	})

	want := []string{"thinking_delta:先看看构建脚本", "delta:构建是干净的，接着看部署。"}
	if got := frameSummary(replayed); !equalStrings(got, want) {
		t.Fatalf("late joiner replay = %v, want %v", got, want)
	}
	for _, f := range replayed {
		if !f.Replay {
			t.Fatalf("frame %q is not marked as a replay, so the client would append it instead of merging", f.Type)
		}
	}
}

func TestAgentStreamerAnnotatesAndPersistsToolDuration(t *testing.T) {
	const convID = "conv-tool-duration"
	ms := storetest.New()
	var frames []ServerMessage
	streamer := &AgentStreamer{Store: ms}
	streamer.Run(context.Background(), AgentStreamRequest{
		Backend: frameScriptBackend{events: []agent.StreamEvent{
			{Kind: agent.KindToolUseStart, Content: `{"id":"tool-1","name":"Bash"}`},
			{Kind: agent.KindToolResult, Content: `{"id":"tool-1","name":"Bash","input":{"command":"true"}}`},
		}},
		WorkDir:        t.TempDir(),
		ConversationID: convID,
		Mode:           AgentStreamPerResult,
		Broadcast:      func(msg ServerMessage) { frames = append(frames, msg) },
	})

	if len(frames) < 2 || frames[0].Type != "tool_use_start" || frames[0].ToolStartedAt == 0 {
		t.Fatalf("tool start frame missing timestamp: %#v", frames)
	}
	if frames[1].Type != "tool_result" || frames[1].ToolDurationMS == nil {
		t.Fatalf("tool result frame missing duration: %#v", frames)
	}

	var toolMessage *store.Message
	for i := range ms.Messages[convID] {
		if ms.Messages[convID][i].Role == "tool" {
			toolMessage = &ms.Messages[convID][i]
			break
		}
	}
	if toolMessage == nil {
		t.Fatal("tool message was not persisted")
	}
	var metadata struct {
		DurationMS int64 `json:"duration_ms"`
	}
	if err := json.Unmarshal(toolMessage.Metadata, &metadata); err != nil {
		t.Fatalf("decode tool metadata: %v", err)
	}
	if metadata.DurationMS < 0 {
		t.Fatalf("duration_ms = %d, want non-negative", metadata.DurationMS)
	}
}

// TestAgentStreamerDropsPersistedReplyFromInFlightReplay guards the other half
// of the snapshot: text that already became an assistant row must not come back
// as in-flight narration. A codex turn emits a result per step, so the buffer
// that feeds the snapshot has to be cleared at each one — otherwise a late
// joiner renders the same paragraph twice, once from REST and once as a live
// stream row.
func TestAgentStreamerDropsPersistedReplyFromInFlightReplay(t *testing.T) {
	const convID = "conv-persisted"
	ms := storetest.New()
	b := NewBroadcaster()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !b.StartJob(convID, cancel) {
		t.Fatal("StartJob refused a fresh conversation")
	}
	defer b.EndJob(convID)

	var replayed []ServerMessage
	streamer := &AgentStreamer{Store: ms, Broadcaster: b}
	streamer.Run(ctx, AgentStreamRequest{
		Backend: frameScriptBackend{events: []agent.StreamEvent{
			{Kind: agent.KindDelta, Content: "第一步做完了。"},
			{Kind: agent.KindResult, Content: "第一步做完了。"},
			{Kind: agent.KindDelta, Content: "现在做第二步。"},
			{Kind: agent.KindToolResult, Content: `{"name":"Bash","input":{}}`},
			{Kind: agent.KindDelta, Content: "跑完了。"},
		}},
		WorkDir:        t.TempDir(),
		ConversationID: convID,
		Mode:           AgentStreamPerResult,
		Broadcast: func(msg ServerMessage) {
			if text, _ := msg.Content.(string); msg.Type == "delta" && text == "跑完了。" {
				replayed = joinLate(t, b, convID)
			}
		},
	})

	want := []string{"delta:现在做第二步。"}
	if got := frameSummary(replayed); !equalStrings(got, want) {
		t.Fatalf("late joiner replay = %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// codex emits one result frame per step, so the aggregate reply is a join of
// many chunks. The join has to preserve line boundaries: ExtractArtifacts reads
// whole lines, and a marker welded onto the next step's first line stops being
// a marker at all.
func TestAppendAggregateChunkPreservesLineBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   string
	}{
		{"empty chunks are skipped", []string{"", "只有这一步", ""}, "只有这一步"},
		{"a boundary is inserted", []string{"第一步", "第二步"}, "第一步\n第二步"},
		{"an existing newline is not doubled", []string{"第一步\n", "第二步"}, "第一步\n第二步"},
		{
			"a trailing marker keeps its own line",
			[]string{"验收完成\n[DAYMUG_ARTIFACT ./shot.png]", "随后继续部署。"},
			"验收完成\n[DAYMUG_ARTIFACT ./shot.png]\n随后继续部署。",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf strings.Builder
			for _, chunk := range tc.chunks {
				appendAggregateChunk(&buf, chunk)
			}
			if got := buf.String(); got != tc.want {
				t.Fatalf("aggregate = %q, want %q", got, tc.want)
			}
		})
	}
}

// A browser conversation has no bridge to post a thread notice, so a publish
// marker that resolved to nothing used to reach only the server log. The reply
// still claimed the file was attached, and the agent — which never sees the
// drop — answers "I don't see it" by re-sending the identical marker. The
// notice has to be persisted, not merely broadcast: the user scrolls back to
// this turn to ask why the file never came.
func TestAgentStreamerReportsRejectedArtifactsToWebConversation(t *testing.T) {
	const convID = "conv-artifact-rejected"
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "shot.png"), []byte("\x89PNG\r\n\x1a\nartifact"), 0o600); err != nil {
		t.Fatal(err)
	}

	ms := storetest.New()
	var frames []ServerMessage
	streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster()}
	out := streamer.Run(context.Background(), AgentStreamRequest{
		Backend: frameScriptBackend{events: []agent.StreamEvent{{
			Kind:    agent.KindResult,
			Content: "验收完成\n[DAYMUG_ARTIFACT ./shot.png]\n[DAYMUG_ARTIFACT ./candidate.pdf]",
		}}},
		WorkDir:         workDir,
		ArtifactRootDir: workDir,
		ConversationID:  convID,
		OwnerID:         "owner-1",
		Mode:            AgentStreamPerResult,
		Broadcast:       func(msg ServerMessage) { frames = append(frames, msg) },
	})
	if out.Err != nil {
		t.Fatalf("run error = %v", out.Err)
	}

	// The file that was real still reaches the user; one bad marker must not
	// cost them the artifacts that resolved.
	if len(out.Artifacts) != 1 || out.Artifacts[0].Name != "shot.png" {
		t.Fatalf("artifacts = %+v, want just shot.png", out.Artifacts)
	}
	if len(out.RejectedArtifacts) != 1 || out.RejectedArtifacts[0].Path != "./candidate.pdf" {
		t.Fatalf("rejections = %+v", out.RejectedArtifacts)
	}

	var notice string
	for _, msg := range ms.SnapshotMessages(convID) {
		if msg.Role == "error" && strings.Contains(msg.Content, "未能送达") {
			notice = msg.Content
		}
	}
	if notice == "" {
		t.Fatalf("no persisted notice for the dropped marker; rows = %+v", ms.SnapshotMessages(convID))
	}
	if !strings.Contains(notice, "./candidate.pdf") || !strings.Contains(notice, "file not found") {
		t.Fatalf("notice names neither the marker nor the reason: %q", notice)
	}
	// Shown in a browser, so it must explain itself without handing out the
	// server's filesystem layout.
	if strings.Contains(notice, workDir) {
		t.Fatalf("notice leaks the host path: %q", notice)
	}
	// The live tab is told too, and against the same row the transcript keeps.
	var broadcast bool
	for _, f := range frames {
		if f.Type == "error" && f.Message == notice && f.MessageID != "" {
			broadcast = true
		}
	}
	if !broadcast {
		t.Fatalf("notice never reached the open tab; frames = %+v", frames)
	}
}

// A turn that published nothing must stay quiet: a notice on every clean turn
// would train the user to ignore the one turn that mattered.
func TestAgentStreamerStaysQuietWhenEveryArtifactResolved(t *testing.T) {
	const convID = "conv-artifact-clean"
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "shot.png"), []byte("\x89PNG\r\n\x1a\nartifact"), 0o600); err != nil {
		t.Fatal(err)
	}

	ms := storetest.New()
	streamer := &AgentStreamer{Store: ms, Broadcaster: NewBroadcaster()}
	streamer.Run(context.Background(), AgentStreamRequest{
		Backend: frameScriptBackend{events: []agent.StreamEvent{{
			Kind: agent.KindResult, Content: "验收完成\n[DAYMUG_ARTIFACT ./shot.png]",
		}}},
		WorkDir:         workDir,
		ArtifactRootDir: workDir,
		ConversationID:  convID,
		OwnerID:         "owner-1",
		Mode:            AgentStreamPerResult,
		Broadcast:       func(ServerMessage) {},
	})

	for _, msg := range ms.SnapshotMessages(convID) {
		if msg.Role == "error" {
			t.Fatalf("clean turn produced a notice row: %q", msg.Content)
		}
	}
}
