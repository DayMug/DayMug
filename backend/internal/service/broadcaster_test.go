package service

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBroadcaster_JoinAndBroadcast(t *testing.T) {
	b := NewBroadcaster()

	ch1 := make(chan []byte, 16)
	ch2 := make(chan []byte, 16)
	b.Join("conv-1", "c1", ch1)
	b.Join("conv-1", "c2", ch2)

	b.Broadcast("conv-1", []byte("hello"))

	select {
	case msg := <-ch1:
		if string(msg) != "hello" {
			t.Errorf("ch1 got %q, want %q", msg, "hello")
		}
	case <-time.After(time.Second):
		t.Fatal("ch1 timeout")
	}

	select {
	case msg := <-ch2:
		if string(msg) != "hello" {
			t.Errorf("ch2 got %q, want %q", msg, "hello")
		}
	case <-time.After(time.Second):
		t.Fatal("ch2 timeout")
	}
}

func TestBroadcaster_Leave(t *testing.T) {
	b := NewBroadcaster()

	ch1 := make(chan []byte, 16)
	ch2 := make(chan []byte, 16)
	b.Join("conv-1", "c1", ch1)
	b.Join("conv-1", "c2", ch2)

	b.Leave("conv-1", "c1")
	b.Broadcast("conv-1", []byte("after leave"))

	select {
	case <-ch1:
		t.Fatal("ch1 should not receive after leave")
	case <-time.After(50 * time.Millisecond):
	}

	select {
	case msg := <-ch2:
		if string(msg) != "after leave" {
			t.Errorf("ch2 got %q", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("ch2 timeout")
	}
}

func TestBroadcaster_LeaveCleanup(t *testing.T) {
	b := NewBroadcaster()

	ch := make(chan []byte, 16)
	b.Join("conv-1", "c1", ch)
	b.Leave("conv-1", "c1")

	b.mu.Lock()
	_, exists := b.rooms["conv-1"]
	b.mu.Unlock()
	if exists {
		t.Error("expected room to be cleaned up after last client leaves while idle")
	}
}

func TestBroadcaster_LeaveKeepsRoomWhileBusy(t *testing.T) {
	// When a job is in flight, the room must survive the last client leaving so
	// busy state and the replay buffer persist across a page-refresh window.
	// Deleting it here was the cause of the "Session ID is already in use" bug:
	// the next request didn't see the old job and spawned a duplicate Claude.
	b := NewBroadcaster()

	ch := make(chan []byte, 16)
	b.Join("conv-1", "c1", ch)

	cancelCalled := atomic.Bool{}
	if !b.StartJob("conv-1", func() { cancelCalled.Store(true) }) {
		t.Fatal("StartJob should succeed on a fresh room")
	}

	b.Leave("conv-1", "c1")

	b.mu.Lock()
	_, exists := b.rooms["conv-1"]
	b.mu.Unlock()
	if !exists {
		t.Fatal("expected room kept while busy")
	}
	if !b.IsBusy("conv-1") {
		t.Error("busy state must survive last client leaving")
	}

	b.EndJob("conv-1")

	b.mu.Lock()
	_, exists = b.rooms["conv-1"]
	b.mu.Unlock()
	if exists {
		t.Error("expected room deleted once idle and clientless")
	}
	if cancelCalled.Load() {
		t.Error("EndJob must not call cancel — that's CancelJob's job")
	}
}

func TestBroadcaster_StartJobIsAtomic(t *testing.T) {
	b := NewBroadcaster()

	if !b.StartJob("conv-1", func() {}) {
		t.Fatal("first StartJob should succeed")
	}
	if b.StartJob("conv-1", func() {}) {
		t.Error("second StartJob on a busy conversation must fail")
	}
	if !b.IsBusy("conv-1") {
		t.Error("expected busy after StartJob")
	}

	b.EndJob("conv-1")
	if b.IsBusy("conv-1") {
		t.Error("expected not busy after EndJob")
	}
	if !b.StartJob("conv-1", func() {}) {
		t.Error("StartJob should succeed after EndJob clears state")
	}
	b.EndJob("conv-1")
}

func TestBroadcaster_WaitStartJobClaimsRoomAfterCurrentJob(t *testing.T) {
	b := NewBroadcaster()
	if !b.StartJob("conv-1", func() {}) {
		t.Fatal("first StartJob should succeed")
	}

	claimed := make(chan bool, 1)
	go func() {
		claimed <- b.waitStartJob(context.Background(), "conv-1", func() {})
	}()
	select {
	case <-claimed:
		t.Fatal("waiting job claimed a busy room")
	case <-time.After(20 * time.Millisecond):
	}

	b.EndJob("conv-1")
	select {
	case ok := <-claimed:
		if !ok {
			t.Fatal("waiting job did not claim the released room")
		}
	case <-time.After(time.Second):
		t.Fatal("waiting job was not woken by EndJob")
	}
	if !b.IsBusy("conv-1") {
		t.Fatal("waiting job did not become the room owner")
	}
	b.EndJob("conv-1")
}

func TestBroadcaster_WaitStartJobStopsOnContextCancel(t *testing.T) {
	b := NewBroadcaster()
	if !b.StartJob("conv-1", func() {}) {
		t.Fatal("first StartJob should succeed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if b.waitStartJob(ctx, "conv-1", func() {}) {
		t.Fatal("cancelled waiter claimed the busy room")
	}
	if !b.IsBusy("conv-1") {
		t.Fatal("cancelled waiter disturbed the current room owner")
	}
	b.EndJob("conv-1")
}

func TestBroadcaster_CancelJobInvokesRegisteredCancel(t *testing.T) {
	b := NewBroadcaster()

	if b.CancelJob("nope") {
		t.Error("CancelJob on unknown conversation must return false")
	}

	_, cancel := context.WithCancel(context.Background())
	called := atomic.Bool{}
	wrapped := func() {
		called.Store(true)
		cancel()
	}
	if !b.StartJob("conv-1", wrapped) {
		t.Fatal("StartJob")
	}
	if !b.CancelJob("conv-1") {
		t.Error("CancelJob should return true while a job is registered")
	}
	if !called.Load() {
		t.Error("CancelJob should invoke the registered cancel func")
	}

	b.EndJob("conv-1")
	if b.CancelJob("conv-1") {
		t.Error("CancelJob after EndJob should return false")
	}
}

func TestBroadcaster_SteerJobUsesOnlyCurrentBusyJob(t *testing.T) {
	b := NewBroadcaster()
	if attempted, err := b.SteerJob(context.Background(), "conv-1", "message-1", "hello"); attempted || err != nil {
		t.Fatalf("idle SteerJob = (%v, %v), want (false, nil)", attempted, err)
	}
	if !b.StartJob("conv-1", func() {}) {
		t.Fatal("StartJob")
	}

	type steerCall struct {
		messageID string
		input     string
	}
	calls := make(chan steerCall, 1)
	if !b.SetJobSteerer("conv-1", func(_ context.Context, messageID, input string) error {
		calls <- steerCall{messageID: messageID, input: input}
		return nil
	}) {
		t.Fatal("SetJobSteerer on busy job")
	}
	attempted, err := b.SteerJob(context.Background(), "conv-1", "message-2", "focus on tests")
	if !attempted || err != nil {
		t.Fatalf("SteerJob = (%v, %v), want (true, nil)", attempted, err)
	}
	if got := <-calls; got.messageID != "message-2" || got.input != "focus on tests" {
		t.Fatalf("steer call = %+v", got)
	}

	b.EndJob("conv-1")
	if attempted, err := b.SteerJob(context.Background(), "conv-1", "message-3", "late"); attempted || err != nil {
		t.Fatalf("post-EndJob SteerJob = (%v, %v), want (false, nil)", attempted, err)
	}
}

func TestBroadcaster_AnswersOnlyCurrentBusyJobQuestion(t *testing.T) {
	b := NewBroadcaster()
	if !b.StartJob("conv-1", func() {}) {
		t.Fatal("StartJob")
	}
	calls := make(chan string, 1)
	if !b.SetJobQuestionResponder("conv-1", func(_ context.Context, requestID string, answers map[string][]string) error {
		calls <- requestID + ":" + answers["q1"][0]
		return nil
	}) {
		t.Fatal("SetJobQuestionResponder")
	}
	attempted, err := b.AnswerJobQuestion(context.Background(), "conv-1", "ask-1", map[string][]string{"q1": {"Yes"}})
	if !attempted || err != nil {
		t.Fatalf("AnswerJobQuestion = (%v, %v)", attempted, err)
	}
	if got := <-calls; got != "ask-1:Yes" {
		t.Fatalf("call = %q", got)
	}
	b.EndJob("conv-1")
	if attempted, err := b.AnswerJobQuestion(context.Background(), "conv-1", "ask-1", nil); attempted || err != nil {
		t.Fatalf("post-EndJob answer = (%v, %v)", attempted, err)
	}
}

func TestBroadcaster_WasCancelledOnlyAfterCancelJob(t *testing.T) {
	// WasCancelled is the signal the terminal handler uses to discriminate
	// user-initiated cancels from server-drain / claude-crash exits, so
	// the JSONL rollback only fires when the user actually pressed Cancel.
	// Lock in: false before any cancel, true after CancelJob, and reset
	// to false when StartJob registers the next run.
	b := NewBroadcaster()

	if b.WasCancelled("conv-1") {
		t.Error("unknown conversation must report false")
	}

	_, cancel := context.WithCancel(context.Background())
	if !b.StartJob("conv-1", cancel) {
		t.Fatal("StartJob")
	}
	if b.WasCancelled("conv-1") {
		t.Error("fresh run must start as not-cancelled")
	}
	if !b.CancelJob("conv-1") {
		t.Fatal("CancelJob")
	}
	if !b.WasCancelled("conv-1") {
		t.Error("CancelJob must flip WasCancelled to true")
	}

	b.EndJob("conv-1")
	// The next StartJob resets the flag so a fresh run isn't haunted by
	// a previous turn's cancel.
	_, cancel2 := context.WithCancel(context.Background())
	if !b.StartJob("conv-1", cancel2) {
		t.Fatal("StartJob 2")
	}
	if b.WasCancelled("conv-1") {
		t.Error("StartJob must reset WasCancelled")
	}
}

func TestBroadcaster_ReplayDeliversBufferedEventsToLateJoiner(t *testing.T) {
	// Simulates: tab1 in flight, broadcasts events; tab1 disconnects; tab2
	// reconnects. Tab2 should receive the in-flight events via replay.
	b := NewBroadcaster()

	ch1 := make(chan []byte, 64)
	b.Join("conv-1", "c1", ch1)
	if !b.StartJob("conv-1", func() {}) {
		t.Fatal("StartJob")
	}

	b.Broadcast("conv-1", []byte("delta-a"))
	b.Broadcast("conv-1", []byte("delta-b"))

	// Drain c1 so the channel doesn't conflate live vs replay tests.
	<-ch1
	<-ch1

	// Simulate tab1 refresh: leave then a new client joins.
	b.Leave("conv-1", "c1")

	ch2 := make(chan []byte, 64)
	b.Join("conv-1", "c2", ch2)

	got := drain(ch2, 2, 500*time.Millisecond)
	if len(got) != 2 || string(got[0]) != "delta-a" || string(got[1]) != "delta-b" {
		t.Errorf("expected replay of [delta-a, delta-b], got %v", stringify(got))
	}
}

func TestBroadcaster_RejoinSameClientDoesNotReplay(t *testing.T) {
	b := NewBroadcaster()
	ch := make(chan []byte, 16)
	b.Join("conv-1", "c1", ch)
	b.Broadcast("conv-1", []byte(`{"type":"delta","content":"hello"}`))
	<-ch

	b.Join("conv-1", "c1", ch)

	select {
	case msg := <-ch:
		t.Fatalf("same client rejoin unexpectedly replayed %s", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestBroadcaster_SubscriptionScopesLiveAndReplayFrames(t *testing.T) {
	b := NewBroadcaster()
	if !b.StartJob("conv-1", func() {}) {
		t.Fatal("StartJob")
	}
	defer b.EndJob("conv-1")

	first := make(chan []byte, 16)
	b.JoinSubscription("conv-1", "browser-1", "sub-1", first)
	b.Broadcast("conv-1", []byte(`{"type":"delta","content":"hello"}`))

	var live ServerMessage
	if err := json.Unmarshal(<-first, &live); err != nil {
		t.Fatalf("decode live frame: %v", err)
	}
	if live.ConversationID != "conv-1" || live.SubscriptionID != "sub-1" || live.Replay {
		t.Fatalf("unexpected live scope: %+v", live)
	}

	b.Leave("conv-1", "browser-1")
	second := make(chan []byte, 16)
	b.JoinSubscription("conv-1", "browser-2", "sub-2", second)

	var replay ServerMessage
	if err := json.Unmarshal(<-second, &replay); err != nil {
		t.Fatalf("decode replay frame: %v", err)
	}
	if replay.ConversationID != "conv-1" || replay.SubscriptionID != "sub-2" || !replay.Replay {
		t.Fatalf("unexpected replay scope: %+v", replay)
	}
}

func TestBroadcaster_RejoinUpdatesSubscriptionForFutureFrames(t *testing.T) {
	b := NewBroadcaster()
	ch := make(chan []byte, 16)
	b.JoinSubscription("conv-1", "browser", "sub-old", ch)
	b.Broadcast("conv-1", []byte(`{"type":"delta","content":"old"}`))

	b.JoinSubscription("conv-1", "browser", "sub-new", ch)
	b.Broadcast("conv-1", []byte(`{"type":"delta","content":"new"}`))

	var oldFrame, newFrame ServerMessage
	if err := json.Unmarshal(<-ch, &oldFrame); err != nil {
		t.Fatalf("decode old frame: %v", err)
	}
	if err := json.Unmarshal(<-ch, &newFrame); err != nil {
		t.Fatalf("decode new frame: %v", err)
	}
	if oldFrame.SubscriptionID != "sub-old" {
		t.Fatalf("queued frame subscription = %q, want sub-old", oldFrame.SubscriptionID)
	}
	if newFrame.SubscriptionID != "sub-new" {
		t.Fatalf("future frame subscription = %q, want sub-new", newFrame.SubscriptionID)
	}
}

func TestBroadcaster_ReplayFramesAreMarked(t *testing.T) {
	b := NewBroadcaster()
	ch1 := make(chan []byte, 16)
	b.Join("conv-1", "c1", ch1)
	b.Broadcast("conv-1", []byte(`{"type":"delta","content":"hello"}`))
	<-ch1

	ch2 := make(chan []byte, 16)
	b.Join("conv-1", "c2", ch2)

	var got ServerMessage
	if err := json.Unmarshal(<-ch2, &got); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if !got.Replay || got.Type != "delta" || got.Content != "hello" {
		t.Fatalf("unexpected replay frame: %+v", got)
	}
}

func TestBroadcaster_ReplayClearsOnNewJob(t *testing.T) {
	b := NewBroadcaster()

	ch1 := make(chan []byte, 16)
	b.Join("conv-1", "c1", ch1)
	if !b.StartJob("conv-1", func() {}) {
		t.Fatal("StartJob 1")
	}
	b.Broadcast("conv-1", []byte("old"))
	<-ch1
	b.EndJob("conv-1")

	if !b.StartJob("conv-1", func() {}) {
		t.Fatal("StartJob 2")
	}
	b.Broadcast("conv-1", []byte("new"))
	<-ch1

	ch2 := make(chan []byte, 16)
	b.Join("conv-1", "c2", ch2)

	got := drain(ch2, 1, 200*time.Millisecond)
	if len(got) != 1 || string(got[0]) != "new" {
		t.Errorf("expected replay of [new] only (old cycle should be cleared), got %v", stringify(got))
	}
}

func TestBroadcaster_BroadcastExceptIsNotReplayed(t *testing.T) {
	// user_message echoes go through BroadcastExcept and are also persisted to
	// the store — replaying them on reconnect would duplicate UI rows.
	b := NewBroadcaster()

	ch1 := make(chan []byte, 16)
	b.Join("conv-1", "c1", ch1)
	if !b.StartJob("conv-1", func() {}) {
		t.Fatal("StartJob")
	}

	b.BroadcastExcept("conv-1", "c1", []byte("user-msg"))

	ch2 := make(chan []byte, 16)
	b.Join("conv-1", "c2", ch2)

	select {
	case msg := <-ch2:
		t.Errorf("BroadcastExcept events must not appear in replay, got %q", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

// drain reads up to n messages from ch with a per-read timeout. Returns
// however many it got before timing out — callers assert on length.
func drain(ch <-chan []byte, n int, timeout time.Duration) [][]byte {
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		select {
		case msg := <-ch:
			out = append(out, msg)
		case <-time.After(timeout):
			return out
		}
	}
	return out
}

func stringify(bs [][]byte) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = string(b)
	}
	return out
}

func TestBroadcaster_LastContextUsageReplayedOnLateJoin(t *testing.T) {
	// Between turns the replay buffer is empty (EndJob clears it), but a
	// freshly opened tab still wants to see the latest token-usage bar.
	// SetLastContextUsage caches the most recent context_usage frame and
	// Join replays it.
	b := NewBroadcaster()

	ch1 := make(chan []byte, 16)
	b.Join("conv-1", "c1", ch1)
	if !b.StartJob("conv-1", func() {}) {
		t.Fatal("StartJob")
	}
	usage := []byte(`{"type":"context_usage","content":"{\"used\":100,\"total\":200000}"}`)
	b.Broadcast("conv-1", usage)
	b.SetLastContextUsage("conv-1", usage)
	<-ch1 // drain live broadcast
	b.EndJob("conv-1")

	// Late tab joins between turns. Replay buffer is empty, so the bar is
	// only available via the cached lastContextUsage.
	ch2 := make(chan []byte, 16)
	b.Join("conv-1", "c2", ch2)

	got := drain(ch2, 1, 200*time.Millisecond)
	if len(got) != 1 || !bytes.Equal(got[0], usage) {
		t.Errorf("expected cached context_usage on late join, got %v", stringify(got))
	}
}

func TestBroadcaster_LastContextUsageReplacedByNewer(t *testing.T) {
	b := NewBroadcaster()
	first := []byte(`{"type":"context_usage","content":"{\"used\":100}"}`)
	second := []byte(`{"type":"context_usage","content":"{\"used\":200}"}`)
	b.SetLastContextUsage("conv-1", first)
	b.SetLastContextUsage("conv-1", second)

	ch := make(chan []byte, 16)
	b.Join("conv-1", "c1", ch)

	got := drain(ch, 1, 200*time.Millisecond)
	if len(got) != 1 || !bytes.Equal(got[0], second) {
		t.Errorf("expected newer cache on join, got %v", stringify(got))
	}
}

func TestBroadcaster_HasContextUsageReportsCacheState(t *testing.T) {
	b := NewBroadcaster()
	if b.HasContextUsage("conv-1") {
		t.Fatal("HasContextUsage should be false on a non-existent room")
	}
	// Joining creates a Room but doesn't seed the cache.
	ch := make(chan []byte, 16)
	b.Join("conv-1", "c1", ch)
	if b.HasContextUsage("conv-1") {
		t.Fatal("HasContextUsage should be false until SetLastContextUsage runs")
	}
	b.SetLastContextUsage("conv-1", []byte(`{"type":"context_usage","content":"{}"}`))
	if !b.HasContextUsage("conv-1") {
		t.Fatal("HasContextUsage should be true after SetLastContextUsage")
	}
}

func TestBroadcaster_BroadcastToNonexistentRoom(t *testing.T) {
	b := NewBroadcaster()
	// Should not panic
	b.Broadcast("no-room", []byte("data"))
}

func TestBroadcaster_DropOnFullChannel(t *testing.T) {
	b := NewBroadcaster()

	// Channel with buffer of 1
	ch := make(chan []byte, 1)
	b.Join("conv-1", "c1", ch)

	// Fill the channel
	b.Broadcast("conv-1", []byte("first"))
	// This should not block even though channel is full
	b.Broadcast("conv-1", []byte("second"))

	msg := <-ch
	if string(msg) != "first" {
		t.Errorf("got %q, want %q", msg, "first")
	}

	select {
	case <-ch:
		t.Fatal("second message should have been dropped")
	default:
	}
}

func TestBroadcaster_BroadcastExceptSkipsSender(t *testing.T) {
	b := NewBroadcaster()

	chSelf := make(chan []byte, 16)
	chOther := make(chan []byte, 16)
	b.Join("conv-1", "self", chSelf)
	b.Join("conv-1", "other", chOther)

	b.BroadcastExcept("conv-1", "self", []byte("echo"))

	select {
	case msg := <-chOther:
		if string(msg) != "echo" {
			t.Errorf("other got %q, want %q", msg, "echo")
		}
	case <-time.After(time.Second):
		t.Fatal("other client should have received echo")
	}

	select {
	case msg := <-chSelf:
		t.Fatalf("self should not have received echo, got %q", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestBroadcaster_BroadcastExceptOnNonexistentRoomIsNoop(t *testing.T) {
	b := NewBroadcaster()
	// Must not panic.
	b.BroadcastExcept("missing", "anyone", []byte("data"))
}

func TestBroadcaster_BroadcastExceptOnSoleSenderDeliversToNobody(t *testing.T) {
	b := NewBroadcaster()
	chSelf := make(chan []byte, 16)
	b.Join("conv-1", "self", chSelf)

	b.BroadcastExcept("conv-1", "self", []byte("alone"))

	select {
	case msg := <-chSelf:
		t.Fatalf("self should not have received echo, got %q", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestBroadcaster_ConcurrentAccess(t *testing.T) {
	b := NewBroadcaster()
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ch := make(chan []byte, 64)
			cid := "c" + string(rune('0'+id))
			b.Join("conv-1", cid, ch)
			for j := 0; j < 10; j++ {
				b.Broadcast("conv-1", []byte("msg"))
			}
			b.Leave("conv-1", cid)
		}(i)
	}

	wg.Wait()
}

// TestBroadcaster_ResetReplay_DropsBufferForLateJoiners verifies the
// fix for the streaming-reorder bug: after a persistence checkpoint
// (persistTool / persistResult) calls ResetReplay, a client that
// reconnects later must not receive the events that have already been
// flushed to the DB. The bug surfaced as the live stream getting
// pinned a few rows above the visible tail because the replay buffer
// was double-rendering tool chips that REST + history_backfill had
// already provided.
func TestBroadcaster_ResetReplay_DropsBufferForLateJoiners(t *testing.T) {
	b := NewBroadcaster()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.StartJob("conv", cancel)

	b.Broadcast("conv", []byte("a"))
	b.Broadcast("conv", []byte("b"))
	b.Broadcast("conv", []byte("c"))

	b.ResetReplay("conv")

	// Late-joiner ch should NOT receive a/b/c — they're "persisted" now
	// and would be redelivered via REST.
	ch := make(chan []byte, 16)
	b.Join("conv", "late", ch)

	select {
	case got := <-ch:
		t.Fatalf("ResetReplay should have cleared the buffer, but late joiner got %q", got)
	case <-time.After(50 * time.Millisecond):
		// Good: nothing replayed.
	}

	// New events post-reset SHOULD still reach late joiners.
	b.Broadcast("conv", []byte("d"))
	select {
	case got := <-ch:
		if string(got) != "d" {
			t.Errorf("expected 'd' after reset, got %q", got)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("post-reset broadcast did not reach late joiner")
	}
	_ = ctx // silence unused
}

// TestBroadcaster_ResetReplay_OnUnknownConversation is a no-op smoke
// test: persistTool / persistResult call ResetReplay unconditionally,
// so it has to be safe on a conversation with no Room.
func TestBroadcaster_ResetReplay_OnUnknownConversation(t *testing.T) {
	b := NewBroadcaster()
	b.ResetReplay("never-existed") // must not panic
}

// seqOf decodes the transport-stamped sequence number off a wire frame.
func seqOf(t *testing.T, frame []byte) uint64 {
	t.Helper()
	var msg ServerMessage
	if err := json.Unmarshal(frame, &msg); err != nil {
		t.Fatalf("unmarshal frame %q: %v", frame, err)
	}
	return msg.Seq
}

func marshalFrame(t *testing.T, msg ServerMessage) []byte {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// Every frame a client receives must carry a strictly increasing seq, and
// the payload must survive the stamping untouched — that pair is what lets
// the client detect a drop without the server having to track per-client
// delivery.
func TestBroadcaster_BroadcastStampsMonotonicSeq(t *testing.T) {
	b := NewBroadcaster()
	ch := make(chan []byte, 16)
	b.Join("conv", "c1", ch)

	for i := 0; i < 3; i++ {
		b.Broadcast("conv", marshalFrame(t, ServerMessage{Type: "delta", Content: "chunk"}))
	}

	for want := uint64(1); want <= 3; want++ {
		select {
		case frame := <-ch:
			if got := seqOf(t, frame); got != want {
				t.Fatalf("seq = %d, want %d", got, want)
			}
			var msg ServerMessage
			_ = json.Unmarshal(frame, &msg)
			if msg.Type != "delta" || msg.Content != "chunk" {
				t.Fatalf("stamping corrupted the frame: %q", frame)
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for frame %d", want)
		}
	}
}

// A reconnecting client's replayed frames and the live frames that follow
// have to form one continuous run: the replay copy is stamped once, at
// broadcast time, not re-stamped on the way out.
func TestBroadcaster_ReplayPreservesOriginalSeq(t *testing.T) {
	b := NewBroadcaster()
	b.StartJob("conv", func() {})
	b.Broadcast("conv", marshalFrame(t, ServerMessage{Type: "delta", Content: "one"}))
	b.Broadcast("conv", marshalFrame(t, ServerMessage{Type: "delta", Content: "two"}))

	ch := make(chan []byte, 16)
	b.JoinSubscription("conv", "late", "sub-1", ch)
	b.Broadcast("conv", marshalFrame(t, ServerMessage{Type: "delta", Content: "three"}))

	for want := uint64(1); want <= 3; want++ {
		select {
		case frame := <-ch:
			if got := seqOf(t, frame); got != want {
				t.Fatalf("seq = %d, want %d (frame %q)", got, want, frame)
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for frame %d", want)
		}
	}
}

// A frame the client can't take must cost it only that frame. Aborting the
// rest of the buffer is what made the drop invisible: the seq proving
// something went missing rides on the frames that follow it.
func TestReplayFrames_SkipsRefusedFrameAndKeepsGoing(t *testing.T) {
	frames := [][]byte{[]byte("a"), []byte("b"), []byte("c"), []byte("d")}
	var delivered []string
	replayFrames(frames, func(data []byte) bool {
		// Stand in for a channel that is full for one frame and drains again
		// right after — the transient case a real slow client hits.
		if string(data) == "b" {
			return false
		}
		delivered = append(delivered, string(data))
		return true
	})

	want := []string{"a", "c", "d"}
	if len(delivered) != len(want) {
		t.Fatalf("delivered %v, want %v", delivered, want)
	}
	for i := range want {
		if delivered[i] != want[i] {
			t.Fatalf("delivered %v, want %v", delivered, want)
		}
	}
}

// End-to-end companion: a joiner whose channel is too small still sees the
// seq jump on the next live frame, which is the signal that drives its
// re-sync.
func TestBroadcaster_SeqExposesDroppedReplayFrames(t *testing.T) {
	b := NewBroadcaster()
	b.StartJob("conv", func() {})
	for i := 0; i < 5; i++ {
		b.Broadcast("conv", marshalFrame(t, ServerMessage{Type: "delta", Content: "chunk"}))
	}

	ch := make(chan []byte, 2)
	b.JoinSubscription("conv", "late", "sub-1", ch)

	if first, second := seqOf(t, <-ch), seqOf(t, <-ch); first != 1 || second != 2 {
		t.Fatalf("first two replayed seqs = %d,%d, want 1,2", first, second)
	}

	b.Broadcast("conv", marshalFrame(t, ServerMessage{Type: "delta", Content: "live"}))
	select {
	case frame := <-ch:
		if got := seqOf(t, frame); got != 6 {
			t.Fatalf("post-drop seq = %d, want 6", got)
		}
	case <-time.After(time.Second):
		t.Fatal("live frame never arrived after a full-channel replay")
	}
}

// The cached context_usage tail is pushed after the replay run but was
// captured before it, so stamping it would drag the client's high-water mark
// backwards and make the next live frame look like a gap. It stays
// unsequenced.
func TestBroadcaster_ContextUsageTailIsUnsequenced(t *testing.T) {
	b := NewBroadcaster()
	usage := marshalFrame(t, ServerMessage{Type: "context_usage", Content: `{"used":1}`})
	b.Broadcast("conv", usage)
	b.SetLastContextUsage("conv", usage)

	ch := make(chan []byte, 8)
	b.JoinSubscription("conv", "fresh", "sub-1", ch)

	select {
	case frame := <-ch:
		if got := seqOf(t, frame); got != 0 {
			t.Fatalf("context_usage tail seq = %d, want 0 (unsequenced)", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for the cached context_usage tail")
	}
}

// Frames that aren't JSON objects (raw payloads used by internal observers
// and several existing tests) must pass through the stamper untouched.
func TestBroadcaster_StampSeqLeavesNonObjectsAlone(t *testing.T) {
	if got := string(stampSeq([]byte("hello"), 7)); got != "hello" {
		t.Fatalf("stampSeq(non-JSON) = %q", got)
	}
	if got := string(stampSeq([]byte("{}"), 7)); got != `{"seq":7}` {
		t.Fatalf("stampSeq(empty object) = %q", got)
	}
	if got := string(stampSeq(nil, 7)); got != "" {
		t.Fatalf("stampSeq(nil) = %q", got)
	}
}

// roomReplayState reads a room's buffer accounting. Reaching into the struct is
// deliberate: the byte cap's whole job is to bound something the public API
// never exposes, so asserting on observable frames alone would pass even if the
// counter drifted and the cap silently stopped firing.
func roomReplayState(t *testing.T, b *Broadcaster, convID string) (frames int, bytes int) {
	t.Helper()
	b.mu.Lock()
	r, ok := b.rooms[convID]
	b.mu.Unlock()
	if !ok {
		t.Fatalf("room %q does not exist", convID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	sum := 0
	for _, f := range r.replay {
		sum += len(f)
	}
	if sum != r.replayBytes {
		t.Fatalf("replayBytes drifted: counter=%d actual=%d", r.replayBytes, sum)
	}
	return len(r.replay), r.replayBytes
}

func TestBroadcaster_ReplayEvictsOldestOnceByteBudgetExceeded(t *testing.T) {
	// A handful of big tool_result frames blows the byte budget long before it
	// comes near replayBufMax frames — the shape the frame-count cap alone
	// could not bound.
	b := NewBroadcaster()
	if !b.StartJob("conv-bytes", func() {}) {
		t.Fatal("StartJob")
	}

	const frameSize = 512 << 10 // 512 KiB, well under the 4 MiB budget on its own
	frames := (replayBufMaxBytes / frameSize) + 4
	for i := 0; i < frames; i++ {
		payload := make([]byte, frameSize)
		payload[0] = byte('a' + i) // make each frame identifiable
		b.Broadcast("conv-bytes", payload)
	}

	gotFrames, gotBytes := roomReplayState(t, b, "conv-bytes")
	if gotBytes > replayBufMaxBytes {
		t.Errorf("replay holds %d bytes, over the %d budget", gotBytes, replayBufMaxBytes)
	}
	if gotFrames >= frames {
		t.Errorf("nothing was evicted: %d frames buffered after %d broadcasts", gotFrames, frames)
	}

	// The newest frame must survive — a late joiner resuming mid-stream needs
	// the tail, not the head.
	b.mu.Lock()
	r := b.rooms["conv-bytes"]
	b.mu.Unlock()
	r.mu.Lock()
	newest := r.replay[len(r.replay)-1]
	r.mu.Unlock()
	if newest[0] != byte('a'+frames-1) {
		t.Errorf("newest frame was evicted: tail marker %q", newest[0])
	}
}

func TestBroadcaster_ReplayKeepsSingleFrameLargerThanBudget(t *testing.T) {
	// One tool_result can carry an entire file, so a frame may exceed the whole
	// budget by itself. Evicting it would leave a reconnecting client with an
	// empty replay while the live stream carried on referring to it.
	b := NewBroadcaster()
	if !b.StartJob("conv-huge", func() {}) {
		t.Fatal("StartJob")
	}

	b.Broadcast("conv-huge", make([]byte, replayBufMaxBytes*2))

	frames, bytes := roomReplayState(t, b, "conv-huge")
	if frames != 1 {
		t.Fatalf("want the oversized frame retained, got %d frames", frames)
	}
	if bytes != replayBufMaxBytes*2 {
		t.Errorf("byte counter = %d, want %d", bytes, replayBufMaxBytes*2)
	}
}

func TestBroadcaster_ReplayByteCounterSurvivesResets(t *testing.T) {
	// Every path that reassigns the buffer must reset the counter with it. If
	// one forgets, the counter only ever grows and the cap starts evicting
	// frames that are still needed (or, reset the wrong way, stops firing).
	b := NewBroadcaster()
	if !b.StartJob("conv-reset", func() {}) {
		t.Fatal("StartJob")
	}
	b.Broadcast("conv-reset", make([]byte, 1024))

	b.ResetReplay("conv-reset")
	if frames, bytes := roomReplayState(t, b, "conv-reset"); frames != 0 || bytes != 0 {
		t.Errorf("after ResetReplay: frames=%d bytes=%d, want 0/0", frames, bytes)
	}

	b.ReplaceReplay("conv-reset", make([]byte, 300), make([]byte, 200))
	if frames, bytes := roomReplayState(t, b, "conv-reset"); frames != 2 || bytes != 500 {
		t.Errorf("after ReplaceReplay: frames=%d bytes=%d, want 2/500", frames, bytes)
	}

	// A room kept alive by a joined client lets us observe EndJob's reset.
	b.Join("conv-reset", "c1", make(chan []byte, 8))
	b.EndJob("conv-reset")
	if frames, bytes := roomReplayState(t, b, "conv-reset"); frames != 0 || bytes != 0 {
		t.Errorf("after EndJob: frames=%d bytes=%d, want 0/0", frames, bytes)
	}
}

func replaySeqs(t *testing.T, frames [][]byte) []uint64 {
	t.Helper()
	out := make([]uint64, 0, len(frames))
	for _, f := range frames {
		var m struct {
			Seq uint64 `json:"seq"`
		}
		if err := json.Unmarshal(f, &m); err != nil {
			t.Fatalf("decode replay frame %q: %v", f, err)
		}
		out = append(out, m.Seq)
	}
	return out
}

func TestBroadcaster_ReplayFrameCapKeepsNewestInOrder(t *testing.T) {
	b := NewBroadcaster()
	if !b.StartJob("conv-cap", func() {}) {
		t.Fatal("StartJob")
	}
	const extra = 1000
	total := replayBufMax + extra
	for i := 0; i < total; i++ {
		b.Broadcast("conv-cap", []byte(`{"type":"delta"}`))
	}

	b.mu.Lock()
	r := b.rooms["conv-cap"]
	b.mu.Unlock()
	r.mu.Lock()
	seqs := replaySeqs(t, r.replay)
	r.mu.Unlock()
	if len(seqs) != replayBufMax {
		t.Fatalf("replay holds %d frames, want %d", len(seqs), replayBufMax)
	}
	for i, s := range seqs {
		if want := uint64(extra + 1 + i); s != want {
			t.Fatalf("replay[%d].seq = %d, want %d — oldest frames must go first, order kept", i, s, want)
		}
	}
}

func TestBroadcaster_ReplayDropReleasesEvictedFrames(t *testing.T) {
	b := NewBroadcaster()
	if !b.StartJob("conv-release", func() {}) {
		t.Fatal("StartJob")
	}
	b.mu.Lock()
	r := b.rooms["conv-release"]
	b.mu.Unlock()

	// Fill past the cap until the live window has spare capacity behind it,
	// so the next drops reslice the same backing array instead of reallocating.
	var backing [][]byte
	for i := 0; i < 4*replayBufMax; i++ {
		b.Broadcast("conv-release", []byte(`{"type":"delta"}`))
		r.mu.Lock()
		if len(r.replay) == replayBufMax && cap(r.replay)-len(r.replay) >= 3 {
			backing = r.replay[:cap(r.replay)]
		}
		r.mu.Unlock()
		if backing != nil {
			break
		}
	}
	if backing == nil {
		t.Fatal("replay buffer never had spare capacity once full")
	}

	for i := 0; i < 3; i++ {
		b.Broadcast("conv-release", []byte(`{"type":"delta"}`))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := 0; i < 3; i++ {
		if backing[i] != nil {
			t.Fatalf("evicted slot %d still references its frame — the backing array retains dropped frames", i)
		}
	}
	if len(r.replay) != replayBufMax || &r.replay[0] != &backing[3] {
		t.Fatal("eviction re-copied the buffer instead of advancing within the backing array")
	}
}

func TestBroadcaster_CountsLiveFramesDroppedForSlowClient(t *testing.T) {
	b := NewBroadcaster()
	b.Join("conv-slow", "c1", make(chan []byte, 1))

	b.Broadcast("conv-slow", []byte(`{"type":"a"}`))
	if got := b.DroppedLiveFrames("conv-slow"); got != 0 {
		t.Fatalf("dropped = %d before the channel filled, want 0", got)
	}
	b.Broadcast("conv-slow", []byte(`{"type":"b"}`))
	b.BroadcastExcept("conv-slow", "", []byte(`{"type":"c"}`))
	if got := b.DroppedLiveFrames("conv-slow"); got != 2 {
		t.Fatalf("dropped = %d, want 2", got)
	}
	if got := b.DroppedLiveFrames("conv-unknown"); got != 0 {
		t.Fatalf("unknown room dropped = %d, want 0", got)
	}
}

func TestRoom_LiveDropLogIsRateLimited(t *testing.T) {
	r := &Room{}
	t0 := time.Unix(1_000_000, 0)
	if !r.noteLiveDropLocked("conv", "c1", t0) {
		t.Fatal("first drop must log")
	}
	if r.noteLiveDropLocked("conv", "c1", t0.Add(time.Second)) {
		t.Fatal("second drop within the interval logged")
	}
	if r.noteLiveDropLocked("conv", "c1", t0.Add(liveDropLogInterval-time.Nanosecond)) {
		t.Fatal("drop just inside the interval logged")
	}
	if r.droppedSinceLog != 2 {
		t.Fatalf("droppedSinceLog = %d, want 2 suppressed drops pending", r.droppedSinceLog)
	}
	if !r.noteLiveDropLocked("conv", "c1", t0.Add(liveDropLogInterval)) {
		t.Fatal("drop after the interval must log again")
	}
	if got := r.droppedLive.Load(); got != 4 {
		t.Fatalf("droppedLive = %d, want 4 — suppressed logs must still count", got)
	}
}
