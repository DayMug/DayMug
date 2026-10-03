package imbot

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func newRecordingResponder() (*progressResponder, *[]string) {
	calls := &[]string{}
	r := &progressResponder{
		start: func(_ context.Context, text string) (string, error) {
			*calls = append(*calls, "start:"+text)
			return "msg-1", nil
		},
		edit: func(_ context.Context, id, text string) error {
			*calls = append(*calls, "edit:"+id+":"+text)
			return nil
		},
		post: func(_ context.Context, chunk string) error {
			*calls = append(*calls, "post:"+chunk)
			return nil
		},
		remove: func(_ context.Context, id string) error {
			*calls = append(*calls, "remove:"+id)
			return nil
		},
	}
	return r, calls
}

func TestProgressResponderUpdateStartsThenEdits(t *testing.T) {
	r, calls := newRecordingResponder()
	ctx := context.Background()

	if err := r.Update(ctx, "hello"); err != nil {
		t.Fatalf("first update: %v", err)
	}
	if err := r.Update(ctx, "world"); err != nil {
		t.Fatalf("second update: %v", err)
	}
	want := []string{"start:hello", "edit:msg-1:world"}
	if got := strings.Join(*calls, "|"); got != strings.Join(want, "|") {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestProgressResponderCompleteSplitsChunks(t *testing.T) {
	r, calls := newRecordingResponder()
	long := strings.Repeat("a", replyChunkLimit) + "tail"

	if err := r.Complete(context.Background(), long); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// First chunk lands in the progress message (via Start since no prior
	// Start happened); the overflow is posted as a fresh reply.
	got := *calls
	if len(got) != 2 || !strings.HasPrefix(got[0], "start:") || got[1] != "post:tail" {
		t.Fatalf("calls = %q", got)
	}
}

func TestProgressResponderCompleteEmptyUsesPlaceholder(t *testing.T) {
	r, calls := newRecordingResponder()
	if err := r.Complete(context.Background(), "   "); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got := *calls; len(got) != 1 || got[0] != "start:"+EmptyReplyText {
		t.Fatalf("calls = %q", got)
	}
}

func TestProgressResponderCompleteAsNewDeletesProgressBeforePostingAnswer(t *testing.T) {
	r, calls := newRecordingResponder()
	ctx := context.Background()
	if err := r.Start(ctx, "working"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := r.completeAsNew(ctx, "done"); err != nil {
		t.Fatalf("complete as new: %v", err)
	}
	want := "start:working|remove:msg-1|post:done"
	if got := strings.Join(*calls, "|"); got != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestProgressResponderCompleteAsNewStopsWhenDeleteFails(t *testing.T) {
	r, calls := newRecordingResponder()
	ctx := context.Background()
	if err := r.Start(ctx, "working"); err != nil {
		t.Fatalf("start: %v", err)
	}
	r.remove = func(_ context.Context, id string) error {
		*calls = append(*calls, "remove:"+id)
		return errors.New("denied")
	}
	if err := r.completeAsNew(ctx, "done"); err == nil {
		t.Fatal("complete as new succeeded despite delete failure")
	}
	if got := strings.Join(*calls, "|"); got != "start:working|remove:msg-1" {
		t.Fatalf("calls = %q, final answer must not post before deletion succeeds", got)
	}
}

func TestProgressResponderRefreshesAndClearsActivity(t *testing.T) {
	r, calls := newRecordingResponder()
	r.refreshActivity = func(context.Context) { *calls = append(*calls, "activity:refresh") }
	r.clearActivity = func(context.Context) { *calls = append(*calls, "activity:clear") }

	ctx := context.Background()
	if err := r.Start(ctx, "working"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := r.Update(ctx, "thinking"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := r.Complete(ctx, "done"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	want := []string{
		"start:working",
		"activity:refresh",
		"edit:msg-1:thinking",
		"activity:refresh",
		"edit:msg-1:done",
		"activity:clear",
	}
	if got := strings.Join(*calls, "|"); got != strings.Join(want, "|") {
		t.Fatalf("calls = %q, want %q", got, strings.Join(want, "|"))
	}
}

// A notice on an editing platform is Update: it supersedes the progress
// message rather than piling a second message onto the thread.
func TestProgressResponderNoticeEditsWhenSupported(t *testing.T) {
	r, calls := newRecordingResponder()
	ctx := context.Background()

	if err := r.Update(ctx, "working"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := r.Notice(ctx, "failed"); err != nil {
		t.Fatalf("notice: %v", err)
	}
	want := "start:working|edit:msg-1:failed"
	if got := strings.Join(*calls, "|"); got != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

// Without an edit primitive Update is silent, so a failure routed through it
// would never reach the reader at all — the notice has to become a message.
func TestProgressResponderNoticePostsWithoutEdit(t *testing.T) {
	var posted []string
	r := &progressResponder{
		start: func(context.Context, string) (string, error) { return "m1", nil },
		post: func(_ context.Context, chunk string) error {
			posted = append(posted, chunk)
			return nil
		},
	}

	if err := r.Notice(context.Background(), "⚠️ 运行失败"); err != nil {
		t.Fatalf("notice: %v", err)
	}
	if len(posted) != 1 || posted[0] != "⚠️ 运行失败" {
		t.Fatalf("posted %q, want the notice", posted)
	}
}

// The indicator is refreshed by the agent stream, which can go silent for a
// whole tool call. Without its own clock it would lapse mid-turn.
func TestProgressResponderHeartbeatRefreshesActivity(t *testing.T) {
	var mu sync.Mutex
	refreshed := make(chan struct{}, 8)
	cleared := 0
	r := &progressResponder{
		start:            func(context.Context, string) (string, error) { return "m1", nil },
		post:             func(context.Context, string) error { return nil },
		refreshActivity:  func(context.Context) { refreshed <- struct{}{} },
		clearActivity:    func(context.Context) { mu.Lock(); cleared++; mu.Unlock() },
		activityInterval: time.Millisecond,
	}

	ctx := context.Background()
	if err := r.Start(ctx, "…"); err != nil {
		t.Fatalf("start: %v", err)
	}
	// One from Start, the rest can only come from the heartbeat.
	for i := 0; i < 3; i++ {
		select {
		case <-refreshed:
		case <-time.After(2 * time.Second):
			t.Fatalf("heartbeat stopped after %d refreshes", i)
		}
	}

	r.Close(ctx)
	mu.Lock()
	got := cleared
	mu.Unlock()
	if got != 1 {
		t.Fatalf("clearActivity called %d times, want 1", got)
	}
	// Close joins the heartbeat goroutine, so nothing may arrive after it.
	drain(refreshed)
	select {
	case <-refreshed:
		t.Fatal("heartbeat still running after Close")
	case <-time.After(20 * time.Millisecond):
	}
}

// A run that fails never reaches Complete, and the bridge closes the responder
// instead. Both paths have to leave the indicator off, and exactly once.
func TestProgressResponderEndsActivityOnceAcrossCompleteAndClose(t *testing.T) {
	cleared := 0
	r := &progressResponder{
		start:         func(context.Context, string) (string, error) { return "m1", nil },
		post:          func(context.Context, string) error { return nil },
		clearActivity: func(context.Context) { cleared++ },
	}

	ctx := context.Background()
	if err := r.Complete(ctx, "done"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	r.Close(ctx)
	if cleared != 1 {
		t.Fatalf("clearActivity called %d times, want 1", cleared)
	}

	failed := &progressResponder{
		start:         func(context.Context, string) (string, error) { return "m1", nil },
		post:          func(context.Context, string) error { return nil },
		clearActivity: func(context.Context) { cleared++ },
	}
	failed.Close(ctx)
	if cleared != 2 {
		t.Fatalf("a failed turn left the indicator on: cleared = %d", cleared)
	}
}

func drain(ch chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// A platform with no edit primitive must not post progress it can never
// revise: the user would be left with a stale "thinking…" above the answer.
func TestProgressResponderWithoutEditStaysSilentUntilComplete(t *testing.T) {
	var posted []string
	activity := 0
	cleared := 0
	r := &progressResponder{
		start: func(_ context.Context, text string) (string, error) {
			posted = append(posted, text)
			return "m1", nil
		},
		post: func(_ context.Context, chunk string) error {
			posted = append(posted, chunk)
			return nil
		},
		refreshActivity: func(context.Context) { activity++ },
		clearActivity:   func(context.Context) { cleared++ },
	}

	ctx := context.Background()
	if err := r.Start(ctx, "thinking"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.Update(ctx, "still thinking"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(posted) != 0 {
		t.Fatalf("progress leaked: %q", posted)
	}
	// The activity indicator is the only signal left, so it must still fire.
	if activity != 2 {
		t.Errorf("refreshActivity called %d times, want 2", activity)
	}

	if err := r.Complete(ctx, "final answer"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(posted) != 1 || posted[0] != "final answer" {
		t.Errorf("posted %q, want just the answer", posted)
	}
	if cleared != 1 {
		t.Errorf("clearActivity called %d times, want 1", cleared)
	}
}

// A connector's size guard belongs to the progress message only: Complete and
// Post carry the answer, and shortening that would drop content the reader has
// no other way to get.
func TestProgressResponderFitsProgressWritesOnly(t *testing.T) {
	var edited, posted []string
	fitted := 0
	r := &progressResponder{
		messageID:  "ts-1",
		fitPreview: func(string) string { fitted++; return "fitted" },
		start:      func(context.Context, string) (string, error) { return "ts-1", nil },
		edit: func(_ context.Context, _, text string) error {
			edited = append(edited, text)
			return nil
		},
		post: func(_ context.Context, chunk string) error {
			posted = append(posted, chunk)
			return nil
		},
	}
	ctx := context.Background()
	if err := r.Update(ctx, "progress"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := r.Complete(ctx, "the answer"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := r.Post(ctx, "an extra chunk"); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if fitted != 1 {
		t.Errorf("fitPreview called %d times, want 1 (the Update)", fitted)
	}
	if want := []string{"fitted", "the answer"}; !reflect.DeepEqual(edited, want) {
		t.Errorf("edits = %q, want %q", edited, want)
	}
	if want := []string{"an extra chunk"}; !reflect.DeepEqual(posted, want) {
		t.Errorf("posts = %q, want %q", posted, want)
	}
}

// Every connector's PostAttachments is this loop, so its contract is theirs:
// uploads run in order, into the responder's own thread, and the first failure
// ends the batch (the bridge uploads one file per call for exactly that reason).
func TestUploadEachStopsAtTheFirstFailure(t *testing.T) {
	msg := Message{ChannelID: "C1", ThreadID: "T1"}
	boom := errors.New("upload refused")
	var uploaded []string
	err := uploadEach(context.Background(), msg,
		[]OutboundAttachment{{Name: "a"}, {Name: "b"}, {Name: "c"}},
		func(_ context.Context, got Message, attachment OutboundAttachment) error {
			if got.ThreadID != msg.ThreadID {
				t.Fatalf("uploaded into thread %q, want %q", got.ThreadID, msg.ThreadID)
			}
			uploaded = append(uploaded, attachment.Name)
			if attachment.Name == "b" {
				return boom
			}
			return nil
		})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the failing upload's error", err)
	}
	if strings.Join(uploaded, ",") != "a,b" {
		t.Fatalf("uploaded = %v, want a then b and nothing after the failure", uploaded)
	}
}
