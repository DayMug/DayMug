package service

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/config"
)

func newTestPool(maxA, maxB int) *Pool {
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "default", MaxConcurrent: maxA},
		},
	}
	if maxB > 0 {
		cfg.Providers = append(cfg.Providers, config.Provider{Name: "alt", MaxConcurrent: maxB})
	}
	return NewPool(cfg)
}

func TestPool_FastPathGrantsImmediately(t *testing.T) {
	p := newTestPool(2, 0)
	ticket, err := p.EnterForUser("", "default", "")
	if err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := ticket.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}

	// Positions chan should be closed already on the fast path.
	if _, open := <-ticket.Positions(); open {
		t.Errorf("expected closed Positions chan on fast path")
	}

	in, q, ok := p.Stats("default")
	if !ok || in != 1 || q != 0 {
		t.Errorf("after grant: in=%d q=%d ok=%v", in, q, ok)
	}
	ticket.Release()
	if in, q, _ := p.Stats("default"); in != 0 || q != 0 {
		t.Errorf("after release: in=%d q=%d", in, q)
	}
}

func TestPool_EmptyAccountUsesFirstConfiguredProvider(t *testing.T) {
	p := NewPool(&config.Config{Providers: []config.Provider{{
		Name: "primary", Type: config.CLITypeClaude, MaxConcurrent: 1,
	}}})
	ticket, err := p.EnterForUser("", "", "")
	if err != nil {
		t.Fatalf("enter first provider: %v", err)
	}
	if err := ticket.Wait(context.Background()); err != nil {
		t.Fatalf("wait first provider: %v", err)
	}
	ticket.Release()
}

func TestPool_QueueAndHandoff(t *testing.T) {
	p := newTestPool(1, 0)
	first, _ := p.EnterForUser("", "default", "")
	if err := first.Wait(context.Background()); err != nil {
		t.Fatalf("first wait: %v", err)
	}

	second, _ := p.EnterForUser("", "default", "")
	// Second is queued — Positions immediately yields position 1.
	select {
	case pos := <-second.Positions():
		if pos != 1 {
			t.Errorf("queued position: got %d want 1", pos)
		}
	case <-time.After(time.Second):
		t.Fatalf("no initial position update for queued ticket")
	}

	// Block on Wait in a goroutine; release the first ticket and verify
	// the second resolves.
	waitDone := make(chan error, 1)
	go func() { waitDone <- second.Wait(context.Background()) }()

	first.Release()

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("second wait: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("second waiter never resolved")
	}

	// Position chan should be closed after grant.
	if _, open := <-second.Positions(); open {
		t.Errorf("expected closed Positions chan after grant")
	}

	second.Release()
	if in, q, _ := p.Stats("default"); in != 0 || q != 0 {
		t.Errorf("after both released: in=%d q=%d", in, q)
	}
}

func TestPool_HandoffPrefersUserWithLessRuntimeInPreviousHour(t *testing.T) {
	p := newTestPool(1, 0)
	now := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }
	runFor := func(userID string, duration time.Duration) {
		t.Helper()
		completed, err := p.EnterForUser(userID, "default", "")
		if err != nil {
			t.Fatalf("seed %s: %v", userID, err)
		}
		if err := completed.Wait(context.Background()); err != nil {
			t.Fatalf("wait for %s: %v", userID, err)
		}
		now = now.Add(duration)
		completed.Release()
	}

	// One long task should outweigh two short tasks. The old completion-count
	// policy chose long-task here because it had completed fewer jobs.
	runFor("long-task", 30*time.Minute)
	runFor("short-tasks", 5*time.Minute)
	runFor("short-tasks", 5*time.Minute)

	holder, err := p.EnterForUser("holder", "default", "")
	if err != nil {
		t.Fatalf("enter holder: %v", err)
	}
	if err := holder.Wait(context.Background()); err != nil {
		t.Fatalf("wait for holder: %v", err)
	}

	longTask, err := p.EnterForUser("long-task", "default", "")
	if err != nil {
		t.Fatalf("queue long-task user: %v", err)
	}
	shortTasks, err := p.EnterForUser("short-tasks", "default", "")
	if err != nil {
		t.Fatalf("queue short-tasks user: %v", err)
	}
	holder.Release()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := shortTasks.Wait(ctx); err != nil {
		t.Fatalf("shorter-runtime user did not bypass longer-runtime user: %v", err)
	}
	select {
	case <-longTask.notify:
		t.Fatal("longer-runtime user was granted first")
	default:
	}

	shortTasks.Release()
	if err := longTask.Wait(ctx); err != nil {
		t.Fatalf("longer-runtime user was not eventually granted: %v", err)
	}
	longTask.Release()
}

func TestPool_UsageWindowCountsOnlyOverlapWithPreviousHour(t *testing.T) {
	p := newTestPool(1, 0)
	now := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	p.recentUsage = []taskUsage{
		{userID: "expired", startedAt: now.Add(-90 * time.Minute), endedAt: now.Add(-60 * time.Minute)},
		{userID: "overlap", startedAt: now.Add(-70 * time.Minute), endedAt: now.Add(-50 * time.Minute)},
		{userID: "recent", startedAt: now.Add(-20 * time.Minute), endedAt: now.Add(-5 * time.Minute)},
	}

	totals := p.usageByUserLocked(now)
	if got := totals["expired"]; got != 0 {
		t.Fatalf("expired usage = %s, want 0", got)
	}
	if got := totals["overlap"]; got != 10*time.Minute {
		t.Fatalf("overlapping usage = %s, want 10m", got)
	}
	if got := totals["recent"]; got != 15*time.Minute {
		t.Fatalf("recent usage = %s, want 15m", got)
	}
	if len(p.recentUsage) != 2 {
		t.Fatalf("retained usage records = %d, want 2", len(p.recentUsage))
	}
}

func TestPool_UsageWindowIncludesActiveTasks(t *testing.T) {
	p := newTestPool(1, 0)
	now := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return now }

	active, err := p.EnterForUser("active-user", "default", "")
	if err != nil {
		t.Fatalf("enter active task: %v", err)
	}
	if err := active.Wait(context.Background()); err != nil {
		t.Fatalf("wait for active task: %v", err)
	}
	now = now.Add(25 * time.Minute)

	totals := p.usageByUserLocked(now)
	if got := totals["active-user"]; got != 25*time.Minute {
		t.Fatalf("active usage = %s, want 25m", got)
	}
	active.Release()
}

func TestPool_CancelWhileQueued(t *testing.T) {
	p := newTestPool(1, 0)
	holder, _ := p.EnterForUser("", "default", "")
	_ = holder.Wait(context.Background())
	defer holder.Release()

	queued, _ := p.EnterForUser("", "default", "")
	ctx, cancel := context.WithCancel(context.Background())

	waitErr := make(chan error, 1)
	go func() { waitErr <- queued.Wait(ctx) }()

	// Drain the initial position update so we know the ticket is enqueued.
	<-queued.Positions()

	cancel()

	select {
	case err := <-waitErr:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel never propagated")
	}

	if _, q, _ := p.Stats("default"); q != 0 {
		t.Errorf("queue should be empty after cancel, got %d", q)
	}
}

// Cancellation has to beat a simultaneous grant. In production this is the
// boundary between a Slack turn still shown as queued in DayMug and the next
// task releasing its account slot; publishing the result after the user hit
// Cancel is worse than leaving the slot idle for one handoff.
func TestPool_CanceledContextWinsGrantedTicket(t *testing.T) {
	const iterations = 64
	for i := 0; i < iterations; i++ {
		p := newTestPool(1, 0)
		ticket, err := p.EnterForUser("", "default", "")
		if err != nil {
			t.Fatalf("iteration %d: enter: %v", i, err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := ticket.Wait(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("iteration %d: wait error = %v, want context.Canceled", i, err)
		}
		// Wait owns the cancellation cleanup even though the ticket had already
		// been granted. Release remains safe for callers that defer it early.
		ticket.Release()
		if inUse, queued, _ := p.Stats("default"); inUse != 0 || queued != 0 {
			t.Fatalf("iteration %d: pool stats = (%d, %d), want empty", i, inUse, queued)
		}
	}
}

// waitOutcome records how one Ticket.Wait ended, including a panic, so the
// race test can count failures across many iterations instead of aborting the
// whole package on the first one.
type waitOutcome struct {
	err      error
	panicked any
}

func waitFor(tk *Ticket, ctx context.Context) (out waitOutcome) {
	defer func() {
		if r := recover(); r != nil {
			out.panicked = r
		}
	}()
	out.err = tk.Wait(ctx)
	return out
}

// TestPool_CancelRacesGrant pins the crash that could take the whole server
// down: handing a slot to the head of the queue and canceling that waiter's
// context are independent events, so both of Wait's select branches can be
// ready at once and the runtime picks one at random. Landing on the cancel
// branch therefore does NOT mean the slot went elsewhere — the ticket may
// already own it. Getting that wrong closes the position channel twice
// (panic, and nothing in the backend recovers) or walks away from a charged
// slot (permanent leak — MaxConcurrent defaults to 1, so one leak wedges the
// account for good).
//
// The two release modes cover both caller shapes: one arms
// `defer ticket.Release()` before Wait, the other drops the ticket and returns
// the error. Both must leave the account idle.
func TestPool_CancelRacesGrant(t *testing.T) {
	const iterations = 200

	raceModes := []struct {
		name string
		// run makes the grant and the cancellation both pending, then
		// reports how queued.Wait resolved.
		run func(queued, holder *Ticket, ctx context.Context, cancel context.CancelFunc) waitOutcome
	}{
		{
			// Deterministic: both select branches are armed before Wait is
			// even entered, so roughly half the iterations take the cancel
			// branch on a ticket that already holds the slot.
			name: "armed-before-wait",
			run: func(queued, holder *Ticket, ctx context.Context, cancel context.CancelFunc) waitOutcome {
				holder.Release()
				cancel()
				return waitFor(queued, ctx)
			},
		},
		{
			// The shape production actually hits: the cancel and the handoff
			// come from different goroutines while Wait is parked.
			name: "concurrent-goroutines",
			run: func(queued, holder *Ticket, ctx context.Context, cancel context.CancelFunc) waitOutcome {
				start := make(chan struct{})
				done := make(chan waitOutcome, 1)
				go func() {
					<-start
					done <- waitFor(queued, ctx)
				}()
				go func() {
					<-start
					cancel()
				}()
				close(start)
				holder.Release()
				return <-done
			},
		},
	}

	releaseModes := []struct {
		name string
		// releaseOnError tells whether the caller still calls Release after
		// Wait failed, as a deferred Release does.
		releaseOnError bool
	}{
		{name: "caller-always-releases", releaseOnError: true},
		{name: "caller-drops-on-error", releaseOnError: false},
	}

	for _, race := range raceModes {
		for _, rel := range releaseModes {
			t.Run(race.name+"/"+rel.name, func(t *testing.T) {
				var panics, leaks int
				var firstPanic any
				for i := 0; i < iterations; i++ {
					p := newTestPool(1, 0)
					holder, err := p.EnterForUser("", "default", "")
					if err != nil {
						t.Fatalf("enter holder: %v", err)
					}
					if err := holder.Wait(context.Background()); err != nil {
						t.Fatalf("holder wait: %v", err)
					}
					queued, err := p.EnterForUser("", "default", "")
					if err != nil {
						t.Fatalf("enter queued: %v", err)
					}
					// Draining the initial update proves the ticket is really
					// in the queue before we start racing.
					if pos := <-queued.Positions(); pos != 1 {
						t.Fatalf("queued position: got %d want 1", pos)
					}

					ctx, cancel := context.WithCancel(context.Background())
					out := race.run(queued, holder, ctx, cancel)
					cancel()

					if out.panicked != nil {
						panics++
						if firstPanic == nil {
							firstPanic = out.panicked
						}
						continue
					}
					if out.err == nil || rel.releaseOnError {
						queued.Release()
					}
					if in, q, _ := p.Stats("default"); in != 0 || q != 0 {
						leaks++
					}
				}
				if panics > 0 {
					t.Errorf("Wait panicked in %d/%d iterations; first: %v", panics, iterations, firstPanic)
				}
				if leaks > 0 {
					t.Errorf("slot leaked in %d/%d iterations", leaks, iterations)
				}
			})
		}
	}
}

func TestPool_UnknownAccount(t *testing.T) {
	p := newTestPool(1, 0)
	_, err := p.EnterForUser("", "no-such", "")
	if !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("expected ErrAccountNotFound, got %v", err)
	}
	if got := ErrAccountNotFound.Error(); got != "provider account not found" {
		t.Errorf("ErrAccountNotFound = %q, want provider-neutral message", got)
	}
}

// AccountForType matches by (name, type). A user binding pointing at a
// provider of the wrong type fails fast so the handler can surface
// "you're bound to a claude account but tried to start a codex
// conversation" cleanly.
func TestPool_AccountForType(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "default", Type: config.CLITypeClaude, MaxConcurrent: 1},
			{Name: "default-codex", Type: config.CLITypeCodex, MaxConcurrent: 1, ConfigDir: "/tmp/codex"},
		},
	}
	p := NewPool(cfg)

	// Exact match by name + type.
	acc, err := p.AccountForType("default", config.CLITypeClaude)
	if err != nil || acc == nil || acc.Name != "default" {
		t.Errorf("by-name claude: acc=%+v err=%v", acc, err)
	}

	// Empty name is refused for every type: an explicit binding is required,
	// there is no implicit fallback to the "default" provider.
	if _, err := p.AccountForType("", config.CLITypeClaude); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("empty-name claude: expected ErrAccountNotFound, got %v", err)
	}
	if _, err := p.AccountForType("", config.CLITypeCodex); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("empty-name codex: expected ErrAccountNotFound, got %v", err)
	}

	// Cross-type lookup: "default" with type=codex fails — the named entry
	// is claude-typed. The handler will surface this as a stale binding.
	if _, err := p.AccountForType("default", config.CLITypeCodex); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("expected ErrAccountNotFound for cross-type lookup, got %v", err)
	}

	// No provider of the requested type configured at all.
	cfgClaudeOnly := &config.Config{
		Providers: []config.Provider{
			{Name: "default", Type: config.CLITypeClaude, MaxConcurrent: 1},
		},
	}
	pClaudeOnly := NewPool(cfgClaudeOnly)
	_, err = pClaudeOnly.AccountForType("", config.CLITypeCodex)
	if !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("expected ErrAccountNotFound when no codex provider configured, got %v", err)
	}
}

func TestPool_PositionShiftsOnHandoff(t *testing.T) {
	p := newTestPool(1, 0)
	holder, _ := p.EnterForUser("", "default", "")
	_ = holder.Wait(context.Background())

	t1, _ := p.EnterForUser("", "default", "") // pos 1
	t2, _ := p.EnterForUser("", "default", "") // pos 2

	// Drain initial positions.
	if pos := <-t1.Positions(); pos != 1 {
		t.Errorf("t1 initial: got %d want 1", pos)
	}
	if pos := <-t2.Positions(); pos != 2 {
		t.Errorf("t2 initial: got %d want 2", pos)
	}

	// Release the holder; t1 gets the slot, t2 should drop to position 1.
	holder.Release()

	// Wait for t1 to be granted.
	if err := t1.Wait(context.Background()); err != nil {
		t.Fatalf("t1 wait: %v", err)
	}

	// t2 should receive a new position update (1) within a reasonable timeout.
	deadline := time.After(time.Second)
	for {
		select {
		case pos, open := <-t2.Positions():
			if !open {
				t.Fatalf("t2 Positions closed unexpectedly")
			}
			if pos == 1 {
				goto done
			}
		case <-deadline:
			t.Fatalf("t2 never shifted to position 1")
		}
	}
done:
	t1.Release()
	_ = t2.Wait(context.Background())
	t2.Release()
}

func TestPool_PositionExcludesRunningTasks(t *testing.T) {
	p := newTestPool(4, 0)
	holders := make([]*Ticket, 4)
	for i := range holders {
		holders[i], _ = p.EnterForUser("", "default", "")
		_ = holders[i].Wait(context.Background())
	}

	next, err := p.EnterForUser("", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	waiter, err := p.EnterForUser("", "default", "")
	if err != nil {
		t.Fatal(err)
	}
	if position := <-next.Positions(); position != 1 {
		t.Fatalf("next position = %d, want first waiting position", position)
	}
	if ahead, running := next.QueueCounts(1); ahead != 4 || running != 4 {
		t.Fatalf("next counts = ahead %d running %d, want ahead 4 running 4", ahead, running)
	}
	if position := <-waiter.Positions(); position != 2 {
		t.Fatalf("waiter position = %d, want second waiting position", position)
	}
	if ahead, running := waiter.QueueCounts(2); ahead != 5 || running != 4 {
		t.Fatalf("waiter counts = ahead %d running %d, want ahead 5 running 4", ahead, running)
	}

	holders[0].Release()
	if err := next.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if position := <-waiter.Positions(); position != 1 {
		t.Fatalf("shifted position = %d, want first waiting position", position)
	}
	if ahead, running := waiter.QueueCounts(1); ahead != 4 || running != 4 {
		t.Fatalf("shifted counts = ahead %d running %d, want ahead 4 running 4", ahead, running)
	}
	for _, holder := range holders[1:] {
		holder.Release()
	}
	if err := waiter.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	waiter.Release()
	next.Release()
}

// Concurrency stress: spin up N tickets, each Wait/Release with a tiny
// amount of work. Ensures the pool handles handoff under load without
// races (run under -race).
func TestPool_Concurrency(t *testing.T) {
	p := newTestPool(3, 0)
	var wg sync.WaitGroup
	const N = 20
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tk, err := p.EnterForUser("", "default", "")
			if err != nil {
				t.Errorf("enter: %v", err)
				return
			}
			if err := tk.Wait(context.Background()); err != nil {
				t.Errorf("wait: %v", err)
				return
			}
			time.Sleep(time.Millisecond)
			tk.Release()
		}()
	}
	wg.Wait()
	if in, q, _ := p.Stats("default"); in != 0 || q != 0 {
		t.Errorf("after concurrency stress: in=%d q=%d", in, q)
	}
}

func TestPool_UserConcurrencyAcrossAccounts(t *testing.T) {
	p := NewPool(&config.Config{
		Users: config.UsersConfig{MaxConcurrent: 2},
		Providers: []config.Provider{
			{Name: "default", MaxConcurrent: 3},
			{Name: "alt", MaxConcurrent: 3},
		},
	})

	first, err := p.EnterForUser("u1", "default", "")
	if err != nil {
		t.Fatalf("first enter: %v", err)
	}
	second, err := p.EnterForUser("u1", "alt", "")
	if err != nil {
		t.Fatalf("second enter: %v", err)
	}
	if err := first.Wait(context.Background()); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	if err := second.Wait(context.Background()); err != nil {
		t.Fatalf("second wait: %v", err)
	}

	queued, err := p.EnterForUser("u1", "default", "")
	if err != nil {
		t.Fatalf("queued enter: %v", err)
	}
	select {
	case pos := <-queued.Positions():
		if pos != 1 {
			t.Fatalf("queued position = %d, want 1", pos)
		}
	case <-time.After(time.Second):
		t.Fatal("user-limited task did not queue")
	}

	// A saturated user must not waste otherwise-free account capacity.
	other, err := p.EnterForUser("u2", "default", "")
	if err != nil {
		t.Fatalf("other user enter: %v", err)
	}
	if err := other.Wait(context.Background()); err != nil {
		t.Fatalf("other user wait: %v", err)
	}

	first.Release()
	if err := queued.Wait(context.Background()); err != nil {
		t.Fatalf("queued task was not granted after user slot release: %v", err)
	}
	if got := p.userInUse["u1"]; got != 2 {
		t.Fatalf("u1 in use = %d, want 2", got)
	}

	second.Release()
	queued.Release()
	other.Release()
	if len(p.userInUse) != 0 {
		t.Fatalf("user slot accounting leaked: %#v", p.userInUse)
	}
}

func TestPool_UserConcurrencyDefaultsToFour(t *testing.T) {
	p := NewPool(&config.Config{Providers: []config.Provider{{Name: "default", MaxConcurrent: 5}}})
	tickets := make([]*Ticket, 0, config.DefaultUserMaxConcurrent)
	for range config.DefaultUserMaxConcurrent {
		ticket, err := p.EnterForUser("u1", "default", "")
		if err != nil {
			t.Fatalf("enter: %v", err)
		}
		if err := ticket.Wait(context.Background()); err != nil {
			t.Fatalf("wait: %v", err)
		}
		tickets = append(tickets, ticket)
	}

	queued, err := p.EnterForUser("u1", "default", "")
	if err != nil {
		t.Fatalf("queued enter: %v", err)
	}
	select {
	case pos := <-queued.Positions():
		if pos != 1 {
			t.Fatalf("queued position = %d, want 1", pos)
		}
	case <-time.After(time.Second):
		t.Fatal("fifth task did not queue at the default user limit")
	}

	tickets[0].Release()
	if err := queued.Wait(context.Background()); err != nil {
		t.Fatalf("queued task was not granted: %v", err)
	}
	for _, ticket := range tickets[1:] {
		ticket.Release()
	}
	queued.Release()
}

func TestPool_CooldownBlocksEnter(t *testing.T) {
	p := newTestPool(2, 0)
	until := time.Now().Add(time.Hour)
	p.Cooldown("default", "", until)

	_, err := p.EnterForUser("", "default", "")
	var ce *CooldownError
	if !errors.As(err, &ce) {
		t.Fatalf("Enter during cooldown = %v, want *CooldownError", err)
	}
	if !ce.Until.Equal(until) {
		t.Errorf("CooldownError.Until = %v, want %v", ce.Until, until)
	}
	if ce.Account != "default" {
		t.Errorf("CooldownError.Account = %q, want %q", ce.Account, "default")
	}

	// A blocked Enter must not consume a slot.
	if in, q, _ := p.Stats("default"); in != 0 || q != 0 {
		t.Errorf("blocked Enter leaked state: in=%d q=%d", in, q)
	}
}

func TestPool_CooldownIsPerModel(t *testing.T) {
	// The bug: exhausting one model's quota (fable) cooled down the whole
	// account, locking the user out of another model (opus) that still had
	// capacity. Cooldown must be scoped to the model that was denied.
	p := newTestPool(2, 0)
	until := time.Now().Add(time.Hour)
	p.Cooldown("default", "fable", until)

	// The throttled model is refused.
	if _, err := p.EnterForUser("", "default", "fable"); err == nil {
		t.Fatalf("Enter on rate-limited model should be refused")
	}

	// A different model on the SAME account is admitted.
	ticket, err := p.EnterForUser("", "default", "opus")
	if err != nil {
		t.Fatalf("Enter on a model with quota should succeed, got %v", err)
	}
	if err := ticket.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
	ticket.Release()

	// The default (empty-model) bucket is likewise unaffected.
	if _, active := p.CooldownUntil("default", ""); active {
		t.Errorf("default-model bucket should not be cooling down")
	}
	if _, active := p.CooldownUntil("default", "fable"); !active {
		t.Errorf("fable bucket should still be cooling down")
	}
}

func TestPool_CooldownExpiresLazily(t *testing.T) {
	p := newTestPool(1, 0)
	// A window already in the past must not block, and Enter clears it.
	p.Cooldown("default", "", time.Now().Add(-time.Second))

	ticket, err := p.EnterForUser("", "default", "")
	if err != nil {
		t.Fatalf("Enter after elapsed cooldown: %v", err)
	}
	if err := ticket.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}
	ticket.Release()

	if _, active := p.CooldownUntil("default", ""); active {
		t.Errorf("expected elapsed cooldown to be cleared")
	}
}

func TestPool_CooldownUntilReports(t *testing.T) {
	p := newTestPool(1, 0)
	if _, active := p.CooldownUntil("default", ""); active {
		t.Errorf("fresh account should not be cooling down")
	}

	until := time.Now().Add(30 * time.Minute)
	p.Cooldown("default", "", until)
	got, active := p.CooldownUntil("default", "")
	if !active || !got.Equal(until) {
		t.Errorf("CooldownUntil = (%v, %v), want (%v, true)", got, active, until)
	}

	// A zero time clears it (latest call wins).
	p.Cooldown("default", "", time.Time{})
	if _, active := p.CooldownUntil("default", ""); active {
		t.Errorf("zero-time Cooldown should clear the window")
	}
}

func TestPool_CooldownLeavesInflightAlone(t *testing.T) {
	p := newTestPool(1, 0)
	ticket, err := p.EnterForUser("", "default", "")
	if err != nil {
		t.Fatalf("enter: %v", err)
	}
	if err := ticket.Wait(context.Background()); err != nil {
		t.Fatalf("wait: %v", err)
	}

	// Cooldown set mid-flight gates only new entries; the live ticket
	// releases normally.
	p.Cooldown("default", "", time.Now().Add(time.Hour))
	ticket.Release()
	if in, q, _ := p.Stats("default"); in != 0 || q != 0 {
		t.Errorf("after in-flight release during cooldown: in=%d q=%d", in, q)
	}

	if _, err := p.EnterForUser("", "default", ""); err == nil {
		t.Errorf("new Enter during cooldown should be refused")
	}
}

// TestPool_QueueLimitRefusesOverflow pins the bounded-queue behaviour that
// keeps a runaway producer (an overlapping cron task, historically) from
// burying interactive prompts under an arbitrarily deep FIFO.
func TestPool_QueueLimitRefusesOverflow(t *testing.T) {
	p := newTestPool(1, 0)
	limit := p.accounts["default"].queueLimit()
	if limit != accountQueueDepthFloor {
		t.Fatalf("queueLimit for max=1 = %d, want the floor %d", limit, accountQueueDepthFloor)
	}

	holder, err := p.EnterForUser("", "default", "")
	if err != nil {
		t.Fatalf("holder enter: %v", err)
	}
	if err := holder.Wait(context.Background()); err != nil {
		t.Fatalf("holder wait: %v", err)
	}

	before := runtime.NumGoroutine()
	waiters := make([]*Ticket, 0, limit)
	for i := 0; i < limit; i++ {
		tk, err := p.EnterForUser("", "default", "")
		if err != nil {
			t.Fatalf("waiter %d refused early: %v", i, err)
		}
		waiters = append(waiters, tk)
	}

	overflow, err := p.EnterForUser("", "default", "")
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("overflow enter err = %v, want ErrQueueFull", err)
	}
	if overflow != nil {
		t.Fatalf("refused Enter must not hand back a ticket, got %v", overflow)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("refused Enter leaked goroutines: %d -> %d", before, after)
	}
	if _, queued, _ := p.Stats("default"); queued != limit {
		t.Fatalf("queue depth after refusal = %d, want %d", queued, limit)
	}

	// The refusal must leave the queue intact: releasing the holder still
	// promotes the head of the line.
	holder.Release()
	if err := waiters[0].Wait(context.Background()); err != nil {
		t.Fatalf("head of queue was not granted after refusal: %v", err)
	}
	// A freed queue slot makes room again.
	if _, err := p.EnterForUser("", "default", ""); err != nil {
		t.Fatalf("enter after a slot freed up: %v", err)
	}

	waiters[0].Release()
	for _, tk := range waiters[1:] {
		tk.Release()
	}
}

// TestPool_QueueLimitScalesWithConcurrency documents that the cap is a
// multiple of the account's own concurrency limit, not a flat number: a
// higher-throughput account drains a longer queue in the same wall time.
func TestPool_QueueLimitScalesWithConcurrency(t *testing.T) {
	p := newTestPool(1, 4)
	if got := p.accounts["default"].queueLimit(); got != accountQueueDepthFloor {
		t.Errorf("max=1 limit = %d, want %d", got, accountQueueDepthFloor)
	}
	if got := p.accounts["alt"].queueLimit(); got != 4*accountQueueDepthPerSlot {
		t.Errorf("max=4 limit = %d, want %d", got, 4*accountQueueDepthPerSlot)
	}
}
