package service

import (
	"context"
	"sync"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// blockingTitler holds its GenerateTitle call open so a test can keep a title
// slot occupied while it fires a second attempt.
type blockingTitler struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func newBlockingTitler() *blockingTitler {
	return &blockingTitler{entered: make(chan struct{}, 4), release: make(chan struct{})}
}

func (b *blockingTitler) GenerateTitle(context.Context, []string, agent.TitleAccount) (string, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	b.entered <- struct{}{}
	<-b.release
	return "Generated title", nil
}

func (b *blockingTitler) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// TestAutoTitleLimiterRefusesBeyondCapacity is the limiter contract in
// isolation: admit up to capacity, then refuse without blocking, and count the
// refusals. Blocking instead would be the bug — a queued title still ends up
// forking a CLI, just later.
func TestAutoTitleLimiterRefusesBeyondCapacity(t *testing.T) {
	lim := NewAutoTitleLimiter(2)

	rel1, ok := lim.TryAcquire()
	if !ok {
		t.Fatal("first TryAcquire refused an empty limiter")
	}
	rel2, ok := lim.TryAcquire()
	if !ok {
		t.Fatal("second TryAcquire refused below capacity")
	}
	if _, ok := lim.TryAcquire(); ok {
		t.Fatal("third TryAcquire granted a slot beyond capacity")
	}
	if got := lim.Dropped(); got != 1 {
		t.Fatalf("Dropped = %d, want 1", got)
	}
	if got := lim.InFlight(); got != 2 {
		t.Fatalf("InFlight = %d, want 2", got)
	}

	// Release is idempotent: a double call must not create a phantom slot.
	rel1()
	rel1()
	if got := lim.InFlight(); got != 1 {
		t.Fatalf("InFlight after double release = %d, want 1", got)
	}
	if _, ok := lim.TryAcquire(); !ok {
		t.Fatal("TryAcquire refused after a slot was freed")
	}
	rel2()
}

// TestMaybeAutoTitleSkipsWhenAllTitleSlotsBusy is the fan-out gate: auto-title
// is fired from PersistResult on every assistant result, so without this the
// title generator kept forking full CLI children after the account pool was
// already saturated. A refused attempt must abandon the title, not queue.
func TestMaybeAutoTitleSkipsWhenAllTitleSlotsBusy(t *testing.T) {
	s := newMessagePersistTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, store.User{
		ID: "owner", Name: "Owner", Username: "owner", WorkDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := s.CreateConversation(ctx, "conv1", "", "owner", t.TempDir(), "claude", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := s.SaveMessage(ctx, store.Message{
		ID: "m1", ConversationID: "conv1", Role: "user", Content: "why is the deploy stuck?",
	}); err != nil {
		t.Fatalf("save message: %v", err)
	}

	titler := newBlockingTitler()
	limiter := NewAutoTitleLimiter(1)
	p := &MessagePersister{Store: s, TitleGen: titler, TitleLimiter: limiter}

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		p.MaybeAutoTitle("conv1")
	}()
	<-titler.entered // the first attempt now holds the only slot

	// Second attempt, synchronous: it must return without invoking the
	// generator instead of waiting for the slot.
	p.MaybeAutoTitle("conv1")
	if got := titler.callCount(); got != 1 {
		t.Fatalf("generator calls = %d, want 1 — the second attempt forked anyway", got)
	}
	if got := limiter.Dropped(); got != 1 {
		t.Fatalf("Dropped = %d, want 1", got)
	}

	close(titler.release)
	<-firstDone

	// The admitted attempt still titles the conversation, and its slot is back.
	conv, err := s.GetConversation(ctx, "conv1")
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if conv.Title != "Generated title" {
		t.Fatalf("title = %q, want %q", conv.Title, "Generated title")
	}
	if got := limiter.InFlight(); got != 0 {
		t.Fatalf("InFlight = %d after the run finished, want 0 — the slot leaked", got)
	}
}
