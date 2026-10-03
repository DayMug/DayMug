package imbridge

import (
	"context"
	"sync"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"

	"github.com/DayMug/DayMug/backend/internal/imbot/imbottest"
)

// ctxHonoringStore mimics database/sql: a write handed a cancelled context is
// refused. storetest.Fake ignores ctx entirely, so without this wrapper a test
// cannot tell whether a turn recorded its state through a live context or a
// dead one — which is precisely how the defect below survived.
type ctxHonoringStore struct {
	*storetest.Fake

	mu      sync.Mutex
	refused []string
	cursors []string
}

func (s *ctxHonoringStore) refuse(what string) {
	s.mu.Lock()
	s.refused = append(s.refused, what)
	s.mu.Unlock()
}

func (s *ctxHonoringStore) SetSessionID(ctx context.Context, id, newSessionID string) error {
	if err := ctx.Err(); err != nil {
		s.refuse("SetSessionID")
		return err
	}
	return s.Fake.SetSessionID(ctx, id, newSessionID)
}

func (s *ctxHonoringStore) UpsertBotThread(ctx context.Context, thread store.BotThread) error {
	if err := ctx.Err(); err != nil {
		s.refuse("UpsertBotThread")
		return err
	}
	s.mu.Lock()
	s.cursors = append(s.cursors, thread.LastMessageID)
	s.mu.Unlock()
	return s.Fake.UpsertBotThread(ctx, thread)
}

func (s *ctxHonoringStore) snapshot() (refused, cursors []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.refused...), append([]string(nil), s.cursors...)
}

// TestPreemptedIMTurnStillPersistsWhatItLearned pins the end-of-turn writes to
// a context that outlives the turn.
//
// A superseded turn has its context cancelled by beginIMTurn, and the writes
// that record what the turn learned run after the agent stream returns. Passing
// the turn context to them meant that on every preemption the thread cursor
// never advanced (so the whole thread history was re-imported and re-injected
// on the next message) and the captured CLI session id was dropped (so the next
// turn started a fresh session with no memory of the conversation). Preemption
// is the documented path for a follow-up message in a live thread, so this hit
// the common case, not an edge case.
func TestPreemptedIMTurnStillPersistsWhatItLearned(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	bridge, ms := newInterruptTestBridge(t, 1, started, release)
	addInterruptTestAgent(ms, imbot.PlatformSlack, "agent1", "bot1", t.TempDir())

	tracked := &ctxHonoringStore{Fake: ms}
	bridge.Store = tracked

	firstReplies := &imbottest.ReplyRecorder{}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		bridge.HandleMessage(context.Background(), interruptTestMessage(imbot.PlatformSlack, "agent1", "bot1", "first"), firstReplies)
	}()
	waitForIMRunStart(t, started)

	secondReplies := &imbottest.ReplyRecorder{}
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		bridge.HandleMessage(context.Background(), interruptTestMessage(imbot.PlatformSlack, "agent1", "bot1", "second"), secondReplies)
	}()

	imbottest.WaitForReplyContaining(t, firstReplies, "已被同一话题中的新消息取消")
	waitForIMRunStart(t, started)
	close(release)
	waitForIMHandle(t, firstDone)
	waitForIMHandle(t, secondDone)

	refused, cursors := tracked.snapshot()
	if len(refused) != 0 {
		t.Fatalf("writes refused on a cancelled context: %v — an interrupted turn lost what it learned", refused)
	}
	if len(cursors) == 0 {
		t.Fatal("no thread cursor was ever persisted")
	}
	var sawFirst bool
	for _, cursor := range cursors {
		if cursor == "first" {
			sawFirst = true
		}
	}
	if !sawFirst {
		t.Fatalf("thread cursors = %v, want the preempted turn to still record message %q", cursors, "first")
	}
}
