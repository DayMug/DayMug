package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/DayMug/DayMug/backend/internal/config"
)

// ErrAccountNotFound means the YAML config has no entry matching the
// requested name. Callers (the terminal handler) translate this into a
// user-visible error and refuse to start the job.
var ErrAccountNotFound = errors.New("provider account not found")

// ErrQueueFull means the account's waiting queue is already at its
// depth limit, so EnterForUser refuses the ticket instead of appending to it.
// Callers translate it the same way they translate ErrAccountNotFound: refuse
// the job and tell the user to retry later.
//
// Why a cap at all: waiters used to be an unbounded slice. A scheduled task
// that overruns its interval parks one ticket per fire, and each parked ticket
// also pins a Drainer in-flight entry —
// so a few hundred of them starve interactive users *and* make graceful
// shutdown time out permanently. Failing fast at the door is the only place
// that backlog can be capped without changing the queue's fairness.
var ErrQueueFull = errors.New("account queue is full")

// accountQueueDepthPerSlot is the queue depth allowed per concurrency slot.
// A multiple rather than a flat number, because an account with
// MaxConcurrent=8 legitimately drains a queue eight times faster than one
// with MaxConcurrent=1 and should be allowed a proportionally longer line.
//
// 32 comes from the wait budget on the other end: promptQueueWait gives a
// web/cron prompt 30 minutes to reach the head of the queue, and a typical
// turn runs 1–5 minutes. The last entry of a queue of depth D waits about
// D/max × turn, so at D = 32×max that entry waits 32–160 minutes — already
// past the point where it can succeed. The cap therefore sits where tickets
// stop being useful and start being pure overhead (a Drainer in-flight entry
// plus a position-broadcast goroutine each), and refuses nothing that could
// realistically have been served.
const accountQueueDepthPerSlot = 32

// accountQueueDepthFloor keeps a misconfigured MaxConcurrent (0 or 1) from
// producing a queue so short that a normal multi-tab burst gets bounced.
const accountQueueDepthFloor = 32

// taskUsageWindow is the rolling period used to balance queue handoffs.
// Older work must stop affecting priority so a formerly busy user eventually
// returns to the same footing as everyone else.
const taskUsageWindow = time.Hour

// CooldownError is returned by EnterForUser when the account is in a rate-limit
// cooldown set from claude's own rate_limit_event. It carries the reset time
// so the caller can tell the user exactly when to retry instead of letting
// them keep hammering an account Anthropic has already throttled — repeated
// retries into a live rate limit are precisely what escalates toward a ban.
//
// Model names the model whose quota is exhausted. Anthropic meters the
// faster models (e.g. fable) separately from Opus, so a cooldown is scoped
// to one model: the error reports which so the user can switch to a model
// that still has quota rather than being told the whole account is dead.
type CooldownError struct {
	Account string
	Model   string
	Until   time.Time
}

func (e *CooldownError) Error() string {
	if e.Model != "" {
		return fmt.Sprintf("account %q model %q is rate-limited; try again after %s",
			e.Account, e.Model, e.Until.Format("15:04:05 MST"))
	}
	return fmt.Sprintf("account %q is rate-limited; try again after %s",
		e.Account, e.Until.Format("15:04:05 MST"))
}

// Pool gates agent invocations against both the per-account and per-user
// concurrency limits set in YAML. Each account has its own queue: when either
// limit is full, new callers wait and receive position updates while they wait.
//
// The pool is goroutine-safe and shared across all WebSocket handlers.
type Pool struct {
	mu            sync.Mutex
	accounts      map[string]*accountState
	accountOrder  []*accountState
	userInUse     map[string]int
	userMax       int
	recentUsage   []taskUsage
	activeTickets map[*Ticket]struct{}
	now           func() time.Time
	cfg           *config.Config
	// rateLimits holds the latest plan-window reading per account → window
	// type; see RecordRateLimit. Allocated on first record.
	rateLimits map[string]map[string]rateLimitReading
}

type taskUsage struct {
	userID    string
	startedAt time.Time
	endedAt   time.Time
}

type accountState struct {
	name string
	max  int
	// enabled gates new tickets after an admin deletes the provider. Existing
	// queued/running tickets are allowed to drain against the retained state.
	enabled bool
	// inUse is the number of slots currently held by running jobs.
	inUse int
	// liveInUse counts live interactive terminal (tty-mode) processes
	// parked on this account. A tty session holds a "live" slot for its
	// whole lifetime (connect → teardown), independent of whether it is
	// actively running a turn. The live cap is LiveMultiplier×max so an
	// operator can keep up to twice MaxConcurrent interactive processes
	// alive (mostly idle, parked at the prompt) while still capping
	// simultaneously-executing turns at max via the inUse semaphore above.
	liveInUse int
	// waiters retains enqueue order for tie-breaking. Reported positions are
	// one-based within this queue and deliberately exclude active jobs: with
	// four slots busy, the fifth task is queue position 1, not position 4.
	waiters []*Ticket
	// cooldownUntil maps a model id to the time its rate-limit window
	// reopens. Keyed by model because Anthropic meters Opus separately from
	// the faster models: exhausting one model's quota must not lock the user
	// out of another that still has capacity (the bug this indirection
	// fixes). Set from claude's rate_limit_event resets_at and cleared lazily
	// once a key's window passes. The empty-string key holds the cooldown for
	// runs that didn't pin a model (account default). A missing or past entry
	// means "this model is not cooling down".
	cooldownUntil map[string]time.Time
}

// queueLimit is the maximum number of tickets allowed to wait on this
// account at once. Derived from the account's own concurrency limit so the
// cap scales with how fast the queue actually drains.
func (a *accountState) queueLimit() int {
	limit := a.max * accountQueueDepthPerSlot
	if limit < accountQueueDepthFloor {
		return accountQueueDepthFloor
	}
	return limit
}

// Ticket represents a queued or active reservation on a Claude account
// slot. Callers obtain one from Pool.EnterForUser, observe queue position via
// Positions(), block on Wait until a slot is available, and call Release
// when the job finishes (success, cancel, or error).
//
// All methods are safe to call from any goroutine.
type Ticket struct {
	pool      *Pool
	acc       *accountState
	userID    string
	startedAt time.Time
	notify    chan struct{} // closed when a slot is granted
	pos       chan int      // one-based position in the waiting queue; closed on grant
	// state is guarded by pool.mu rather than by a lock of its own. A
	// ticket's stage and the account's slot accounting (inUse / waiters)
	// have to move as one atomic step: while they were two separate locks a
	// grant and a cancel could each believe they owned the ticket, which
	// double-closed pos (panic) and lost the slot.
	state ticketState
}

type ticketState int

const (
	ticketWaiting ticketState = iota
	ticketGranted
	ticketReleased
	ticketCanceled
)

// Positions returns a channel containing the ticket's one-based position in
// the waiting queue. Running jobs are not included. The channel is closed
// once the slot is granted; receiving from a closed channel yields 0 and
// signals "your turn" (or "ticket canceled" if Wait returned an error).
// The channel buffers a few values so a slow reader doesn't block the pool.
func (t *Ticket) Positions() <-chan int {
	return t.pos
}

// QueueCounts expands a waiting-queue position into the two counts shown to
// users: every task ahead of this ticket, and the subset currently running.
// The position is one-based and includes this ticket, so only position-1
// earlier waiters are added to the active count.
func (t *Ticket) QueueCounts(position int) (ahead, running int) {
	t.pool.mu.Lock()
	defer t.pool.mu.Unlock()

	running = t.acc.inUse
	if position > 0 {
		ahead = running + position - 1
	}
	return ahead, running
}

// Wait blocks until the slot is granted or ctx is canceled. On success
// returns nil; on cancel it unwinds the ticket and returns ctx.Err().
func (t *Ticket) Wait(ctx context.Context) error {
	// A caller that was already cancelled before it started waiting must not
	// win a slot merely because notify is also ready. Go selects randomly when
	// both branches below are armed, which otherwise lets a queued IM turn
	// escape a web cancel at the exact moment the preceding job releases.
	if err := ctx.Err(); err != nil {
		t.abandon(ticketCanceled)
		return err
	}
	select {
	case <-t.notify:
		// The grant and cancel can become ready concurrently while Wait is
		// blocked. Re-check after the grant branch so an already-observable
		// cancellation still wins and the newly granted slot is handed on.
		if err := ctx.Err(); err != nil {
			t.abandon(ticketCanceled)
			return err
		}
		return nil
	case <-ctx.Done():
		// Reaching this branch does NOT prove the slot went to someone
		// else: a grant and a cancel can both be pending, in which case the
		// runtime picks a branch at random. abandon() re-reads the ticket's
		// stage under the pool lock, so a slot that was in fact already
		// ours is handed on to the next waiter instead of being lost —
		// callers routinely drop the ticket without calling Release once
		// Wait returns an error.
		t.abandon(ticketCanceled)
		return ctx.Err()
	}
}

// Release returns the slot back to the pool. Safe to call from several
// cleanup paths and after a failed Wait: every call after the first is a
// no-op.
//
// Note this is deliberately not a sync.Once. Wait's cancel path can also
// return the slot, and it does so outside any Once the ticket owns, so the
// only workable guard is the ticket's terminal state read under pool.mu —
// the same lock that protects the slot accounting being changed.
func (t *Ticket) Release() {
	t.abandon(ticketReleased)
}

// abandon unwinds a ticket its caller is finished with, whatever stage it
// reached, recording `terminal` so later calls are no-ops.
func (t *Ticket) abandon(terminal ticketState) {
	t.pool.mu.Lock()
	defer t.pool.mu.Unlock()

	switch t.state {
	case ticketWaiting:
		// Never got a slot: just step out of line. pos is still open here,
		// because only a grant or another abandon closes it.
		t.state = terminal
		for i, x := range t.acc.waiters {
			if x == t {
				t.acc.waiters = append(t.acc.waiters[:i], t.acc.waiters[i+1:]...)
				break
			}
		}
		close(t.pos)
		t.pool.broadcastPositions(t.acc)
	case ticketGranted:
		// Account and, when present, user slots are charged to this ticket.
		// Return both and schedule every account because releasing a user slot
		// may unblock that user's ticket on a different provider.
		t.state = terminal
		t.pool.releaseLocked(t)
	}
}

// NewPool returns a Pool that has one accountState per database-backed account.
// cfg may be nil in tests; in that case the pool refuses every EnterForUser.
func NewPool(cfg *config.Config) *Pool {
	p := &Pool{
		accounts:      map[string]*accountState{},
		userInUse:     map[string]int{},
		userMax:       config.DefaultUserMaxConcurrent,
		activeTickets: map[*Ticket]struct{}{},
		now:           time.Now,
		cfg:           cfg,
	}
	if cfg == nil {
		return p
	}
	if cfg.Users.MaxConcurrent > 0 {
		p.userMax = cfg.Users.MaxConcurrent
	}
	for _, acc := range cfg.ProviderSnapshot() {
		state := &accountState{
			name:          acc.Name,
			max:           acc.MaxConcurrent,
			enabled:       true,
			cooldownUntil: map[string]time.Time{},
		}
		p.accounts[acc.Name] = state
		p.accountOrder = append(p.accountOrder, state)
	}
	return p
}

// Reconfigure applies an admin-edited provider list without interrupting
// tickets that already hold or wait for a slot. Existing account states keep
// their usage/cooldown history; new accounts become available immediately.
// Removed accounts disappear from lookup once idle, while a busy state stays
// reachable only long enough for its existing tickets to release cleanly.
func (p *Pool) Reconfigure(providers []config.Provider) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	wanted := make(map[string]config.Provider, len(providers))
	for _, provider := range providers {
		wanted[provider.Name] = provider
		if state, ok := p.accounts[provider.Name]; ok {
			state.max = provider.MaxConcurrent
			state.enabled = true
			continue
		}
		state := &accountState{
			name:          provider.Name,
			max:           provider.MaxConcurrent,
			enabled:       true,
			cooldownUntil: map[string]time.Time{},
		}
		p.accounts[provider.Name] = state
		p.accountOrder = append(p.accountOrder, state)
	}

	order := p.accountOrder[:0]
	for _, state := range p.accountOrder {
		_, keep := wanted[state.name]
		state.enabled = keep
		if !keep && state.inUse == 0 && state.liveInUse == 0 && len(state.waiters) == 0 {
			delete(p.accounts, state.name)
			continue
		}
		order = append(order, state)
	}
	p.accountOrder = order
}

// AccountForType resolves an account by (name, cliType). cliType must be one
// of config.SupportedCLITypes. name is
// the user's explicit binding for that type and is REQUIRED: an empty name
// no longer falls back to the default provider — it returns
// ErrAccountNotFound so the caller refuses the job and asks the operator to
// bind an account. This is deliberate; the old implicit default fallback let
// unbound users pile onto the shared default ~/.claude login and trip its
// rate limit.
//
// Returns ErrAccountNotFound when name is empty (the user has no binding for
// this type), or when name is non-empty but matches no provider of the
// requested type (a stale binding pointing at a deleted or re-typed account).
func (p *Pool) AccountForType(name, cliType string) (*config.Provider, error) {
	if p.cfg == nil {
		return nil, ErrAccountNotFound
	}
	if name == "" {
		return nil, fmt.Errorf("%w: no account bound for type %q (explicit binding required)", ErrAccountNotFound, cliType)
	}
	acc := p.cfg.FindAccountForType(name, cliType)
	if acc == nil {
		return nil, fmt.Errorf("%w: %q for type %q", ErrAccountNotFound, name, cliType)
	}
	return acc, nil
}

// EnterForUser registers a new ticket on the named account. Returns a Ticket
// immediately so the caller can subscribe to position updates before the
// slot is actually granted. The caller MUST call ticket.Wait(ctx) to block
// until the slot is granted, then ticket.Release() when done.
//
// If a slot is already free, the ticket is granted before EnterForUser returns
// (Wait will succeed immediately and Positions will be a closed channel).
//
// model scopes the rate-limit check: only that model's cooldown gates the
// job, so a job on a model with quota is admitted even while a sibling model
// on the same account is throttled. Empty model uses the account-default
// bucket.
//
// userID additionally applies the process-wide per-user limit. It is
// intentionally independent of the provider account: one user cannot exceed
// the configured cap by spreading work across multiple bound providers. An
// empty userID applies the account limit only.
func (p *Pool) EnterForUser(userID, accountName, model string) (*Ticket, error) {
	if accountName == "" {
		provider, ok := p.cfg.DefaultProvider()
		if !ok {
			return nil, fmt.Errorf("%w: no provider configured", ErrAccountNotFound)
		}
		accountName = provider.Name
	}

	p.mu.Lock()
	acc, ok := p.accounts[accountName]
	if !ok || !acc.enabled {
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: %q", ErrAccountNotFound, accountName)
	}

	// Refuse the job while this model is rate-limited. Clear the cooldown
	// lazily once its window has passed so the next caller proceeds normally.
	if until, cooling := acc.cooldownUntil[model]; cooling && !until.IsZero() {
		if time.Now().Before(until) {
			p.mu.Unlock()
			return nil, &CooldownError{Account: accountName, Model: model, Until: until}
		}
		delete(acc.cooldownUntil, model)
	}

	t := &Ticket{
		pool:   p,
		acc:    acc,
		userID: userID,
		notify: make(chan struct{}),
		pos:    make(chan int, 4),
	}

	if p.canGrantLocked(t) {
		// Fast path: take both slots immediately. notify is already a fresh
		// channel; closing it lets Wait return without blocking.
		p.grantLocked(t)
		p.mu.Unlock()
		return t, nil
	}

	// An account or user slot is busy, so this ticket has to queue — but only
	// if the account queue still has room. Refusing here (rather than appending
	// unconditionally) keeps a runaway producer from burying interactive
	// prompts under an arbitrarily deep queue; see ErrQueueFull.
	if limit := acc.queueLimit(); len(acc.waiters) >= limit {
		queued := len(acc.waiters)
		p.mu.Unlock()
		return nil, fmt.Errorf("%w: %q already has %d waiting (limit %d)",
			ErrQueueFull, accountName, queued, limit)
	}

	acc.waiters = append(acc.waiters, t)
	pos := len(acc.waiters)
	// Push the initial position while still holding the lock so a concurrent
	// releaseSlot can't grant our ticket (closing t.pos) between releasing
	// the lock and the send. The buffer is large enough that this never
	// blocks.
	t.pos <- pos
	p.mu.Unlock()

	return t, nil
}

// canGrantLocked reports whether both dimensions charged to a ticket have
// capacity. Tickets entered with an empty user ID and therefore only
// consume an account slot.
func (p *Pool) canGrantLocked(t *Ticket) bool {
	if t.acc.inUse >= t.acc.max {
		return false
	}
	return t.userID == "" || p.userInUse[t.userID] < p.userMax
}

// grantLocked atomically charges every slot the ticket represents before
// waking its waiter.
func (p *Pool) grantLocked(t *Ticket) {
	t.acc.inUse++
	if t.userID != "" {
		p.userInUse[t.userID]++
	}
	t.startedAt = p.now()
	p.activeTickets[t] = struct{}{}
	t.grantLocked()
}

// grantLocked marks the ticket as owning a slot and wakes its Wait.
//
// Recording the state is not bookkeeping for its own sake: it is the only
// thing that lets a concurrent abandon tell "the slot is already mine" from
// "I'm still queued". Without it the canceling goroutine re-closes pos and
// panics, and the slot it was silently handed is never returned.
//
// MUST be called with p.mu held, in the same critical section that charged
// the slot to this ticket.
func (t *Ticket) grantLocked() {
	t.state = ticketGranted
	// Close position chan first — the waiter's Positions() channel receiver
	// sees it close and stops reading.
	close(t.pos)
	close(t.notify)
}

// releaseLocked returns the ticket's account and user slots, then grants any
// tickets that now fit. Every account is considered because a user slot freed
// on one provider may unblock the same user's task on another.
//
// MUST be called with p.mu held.
func (p *Pool) releaseLocked(t *Ticket) {
	endedAt := p.now()
	if t.acc.inUse > 0 {
		t.acc.inUse--
	}
	if t.userID != "" {
		if p.userInUse[t.userID] <= 1 {
			delete(p.userInUse, t.userID)
		} else {
			p.userInUse[t.userID]--
		}
	}
	if _, active := p.activeTickets[t]; active {
		delete(p.activeTickets, t)
		p.recordUsageLocked(t.userID, t.startedAt, endedAt)
	}
	p.scheduleLocked()
}

// scheduleLocked fills all available account slots. Within each account it
// selects a waiter from the user who consumed the least execution time during
// the previous hour. Enqueue order breaks ties, while the capacity check
// prevents a saturated user from leaving the provider idle while others queue.
func (p *Pool) scheduleLocked() {
	usageByUser := p.usageByUserLocked(p.now())
	for {
		granted := false
		for _, acc := range p.accountOrder {
			if acc.inUse >= acc.max {
				continue
			}
			if i := p.nextWaiterLocked(acc, usageByUser); i >= 0 {
				next := acc.waiters[i]
				acc.waiters = append(acc.waiters[:i], acc.waiters[i+1:]...)
				p.grantLocked(next)
				granted = true
			}
		}
		if !granted {
			break
		}
	}
	for _, acc := range p.accountOrder {
		p.broadcastPositions(acc)
	}
}

// nextWaiterLocked returns the runnable waiter whose user consumed the least
// execution time in the rolling window. Scanning is cheap because each
// account's queue is bounded; a strict comparison preserves FIFO on ties.
func (p *Pool) nextWaiterLocked(acc *accountState, usageByUser map[string]time.Duration) int {
	bestIndex := -1
	var bestUsage time.Duration
	for i, waiter := range acc.waiters {
		if !p.canGrantLocked(waiter) {
			continue
		}
		usage := usageByUser[waiter.userID]
		if bestIndex == -1 || usage < bestUsage {
			bestIndex = i
			bestUsage = usage
		}
	}
	return bestIndex
}

func (p *Pool) recordUsageLocked(userID string, startedAt, endedAt time.Time) {
	if !endedAt.After(startedAt) {
		return
	}
	p.recentUsage = append(p.recentUsage, taskUsage{
		userID:    userID,
		startedAt: startedAt,
		endedAt:   endedAt,
	})
}

// usageByUserLocked totals the portion of every execution interval that
// overlaps [now-taskUsageWindow, now]. Active work is included up to now so a
// long-running task affects the very next handoff even before it completes.
func (p *Pool) usageByUserLocked(now time.Time) map[string]time.Duration {
	windowStart := now.Add(-taskUsageWindow)
	firstRecent := 0
	for firstRecent < len(p.recentUsage) && !p.recentUsage[firstRecent].endedAt.After(windowStart) {
		firstRecent++
	}
	if firstRecent > 0 {
		p.recentUsage = append(p.recentUsage[:0], p.recentUsage[firstRecent:]...)
	}

	totals := make(map[string]time.Duration)
	add := func(userID string, startedAt, endedAt time.Time) {
		if startedAt.Before(windowStart) {
			startedAt = windowStart
		}
		if endedAt.After(now) {
			endedAt = now
		}
		if endedAt.After(startedAt) {
			totals[userID] += endedAt.Sub(startedAt)
		}
	}
	for _, usage := range p.recentUsage {
		add(usage.userID, usage.startedAt, usage.endedAt)
	}
	for ticket := range p.activeTickets {
		add(ticket.userID, ticket.startedAt, now)
	}
	return totals
}

// broadcastPositions sends each caller's new one-based waiting-queue
// position. Non-blocking: if their buffer is full we drop the oldest value
// and push the latest, since only the most recent position is meaningful.
//
// MUST be called with p.mu held.
func (p *Pool) broadcastPositions(acc *accountState) {
	for i, t := range acc.waiters {
		pos := i + 1
		select {
		case t.pos <- pos:
		default:
			select {
			case <-t.pos:
			default:
			}
			select {
			case t.pos <- pos:
			default:
			}
		}
	}
}

// Cooldown marks one model on an account as rate-limited until `until`,
// derived from a claude rate_limit_event. While the window is open EnterForUser
// refuses new tickets for that model with a CooldownError; other models on
// the same account keep flowing. In-flight jobs are left alone — only new
// entries are gated. A call with a zero or past time clears that model's
// cooldown; the latest call wins. Unknown account names are ignored. Empty
// model uses the account-default bucket.
func (p *Pool) Cooldown(accountName, model string, until time.Time) {
	if accountName == "" {
		provider, ok := p.cfg.DefaultProvider()
		if !ok {
			return
		}
		accountName = provider.Name
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	acc, ok := p.accounts[accountName]
	if !ok {
		return
	}
	if until.IsZero() || !time.Now().Before(until) {
		delete(acc.cooldownUntil, model)
		return
	}
	acc.cooldownUntil[model] = until
}

// CooldownUntil reports when a model's rate-limit cooldown expires and
// whether it is currently active. A zero or already-elapsed window reports
// (zero, false). Empty model queries the account-default bucket.
func (p *Pool) CooldownUntil(accountName, model string) (time.Time, bool) {
	if accountName == "" {
		provider, ok := p.cfg.DefaultProvider()
		if !ok {
			return time.Time{}, false
		}
		accountName = provider.Name
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	acc, ok := p.accounts[accountName]
	if !ok {
		return time.Time{}, false
	}
	until, cooling := acc.cooldownUntil[model]
	if !cooling || until.IsZero() || !time.Now().Before(until) {
		return time.Time{}, false
	}
	return until, true
}

// Stats returns the current in-use count and queue length for an account.
// Useful for tests and for an admin observability endpoint later.
func (p *Pool) Stats(accountName string) (inUse, queued int, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	acc, found := p.accounts[accountName]
	if !found {
		return 0, 0, false
	}
	return acc.inUse, len(acc.waiters), true
}
