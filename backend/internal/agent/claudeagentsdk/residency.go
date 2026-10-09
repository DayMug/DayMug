package claudeagentsdk

// Residency keeps one bridge process alive past the end of a turn, but only
// when that turn left background work running.
//
// Why the condition matters as much as the feature: a live agent session on
// this deployment measures 0.7–1.2 GB (docs/development/known-issues.md), and the server has
// already been OOM-killed once for spawn paths nothing was counting. Parking
// every conversation would make the resident fleet proportional to how many
// chats exist; parking only sessions with live background tasks makes it
// proportional to how much background work is actually running, which is the
// thing the feature is for.
//
// The shape, and how it differs from a per-turn spawn:
//
//	spawn ──► residentBridge
//	            ├─ reader: one StreamLines call for the whole process life
//	            └─ router: fans that stream into whichever turn is attached
//	                 ├─ background_tasks_changed → replace the level set
//	                 ├─ a frame with no turn attached → wake a new turn
//	                 ├─ result                    → detach the turn
//	                 └─ everything else           → the attached turn's sink
//
// Compare codexapp/pool.go, whose account-servers are interchangeable. These
// are not: a bridge holds one conversation's live SDK session, so the key is
// the conversation and a mismatch has to retire rather than pick another.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/claudecli"
	"github.com/DayMug/DayMug/backend/internal/agent/residentpool"
	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
)

const (
	// parkDrainGrace is how long a bridge whose background tasks have all
	// settled may sit before it is retired. It absorbs the gap between one
	// task finishing and the wakeup turn it triggers actually starting —
	// retiring on the empty set alone would kill the process in the window
	// between "the work finished" and "the model reacts to it".
	parkDrainGrace = 60 * time.Second
	// parkHardTTL bounds one continuous park whose task set never empties.
	// The agent sees nothing while parked, so a background loop that spins
	// without ever finishing (a poller stuck on an error response, say) would
	// otherwise hold the conversation "running" for as long as it likes. A
	// wakeup turn starts a fresh park, so work that keeps reporting back is
	// not cut off.
	parkHardTTL = 30 * time.Minute
	// backgroundEventSilenceTTL stops provider-managed work that is still
	// reported as live but has produced no stream event for long enough to be
	// indistinguishable from a wedged monitor. The reaper runs once a minute,
	// so enforcement may trail this boundary by at most one interval.
	backgroundEventSilenceTTL = 30 * time.Minute
	// reaperInterval is how often idle bridges are swept. Reclaim also runs
	// on every pool lookup, but a parked conversation nobody returns to would
	// otherwise hold ~1 GB until the next unrelated lookup happened to land.
	reaperInterval = 60 * time.Second
	// interruptGrace bounds both halves of a Stop: how long the bridge has
	// to accept the interrupt control frame, and how long the aborted turn
	// then has to produce its terminal result.
	interruptGrace = 3 * time.Second
	// exitGrace is how long a bridge gets to exit on its own after its stdin
	// closes, before the process group is signalled. Long enough for the SDK
	// to finish writing its session JSONL, short enough that a wedged bridge
	// does not hold up a turn.
	exitGrace = 5 * time.Second
	// defaultMaxParked bounds the resident fleet globally. Four sessions is
	// roughly 4 GB — about a quarter of the 15 GiB box — leaving room for the
	// turns actually executing. Deliberately far below codexapp's 16: those
	// are shared per-account app-servers, these are full agent sessions.
	defaultMaxParked = 4
	// maxParkedEnv is an operator escape hatch, not a product setting: it is
	// read once at startup and never appears in config.yaml. 0 disables
	// residency entirely, which restores the pre-residency behaviour exactly.
	maxParkedEnv = "DAYMUG_AGENT_SDK_MAX_PARKED"
)

// nowFunc is the package clock seam so reclamation tests need not sleep.
var nowFunc = time.Now

var (
	errBridgeRetired = errors.New("resident CAS bridge was retired while idle")
	errPoolClosed    = errors.New("resident CAS bridge pool is shut down")
)

// maxParkedBridges resolves the cap once. A malformed value is treated as
// "operator meant to set something" and falls back to the default rather than
// silently disabling residency.
var maxParkedBridges = func() int {
	raw := strings.TrimSpace(os.Getenv(maxParkedEnv))
	if raw == "" {
		return defaultMaxParked
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		log.Printf("[claude-CAS] ignoring %s=%q: want a non-negative integer", maxParkedEnv, raw)
		return defaultMaxParked
	}
	return n
}()

// residentBridge is one bridge process and the turn currently attached to it.
//
// Locking: sink handover happens under mu, but sends never do. Each attachment
// has its own cancellation channel and sender count: detach first removes and
// cancels that attachment, then waits outside mu for its senders to leave. That
// keeps RunWithSession's "no writes after stream returns" guarantee without
// letting a stalled consumer pin this bridge's lock (and, through pool
// maintenance, every resident Claude conversation).
type residentBridge struct {
	key         string
	fingerprint string
	proc        agent.RunningProcess
	active      *activeBridge
	cancel      context.CancelFunc
	stderr      interface{ String() string }
	stderrWait  func()
	events      chan agent.StreamEvent
	// unregister drops this bridge's control channel from the runner's
	// registry and closes its stdin, which is what lets the SDK end its
	// query cleanly. Called exactly once, from markDead.
	unregister func()
	turns      atomic.Uint64
	teardown   sync.Once
	exitErr    error

	mu      sync.Mutex
	sink    *bridgeSink
	turnEnd chan error
	// expectedTurnID belongs to a user-prompted turn. Provider-initiated
	// background turns have their own result frames and must not settle this
	// waiter merely because they share the same resident query.
	expectedTurnID string
	tasks          int
	// tasksEmptyAt starts the drain grace when the SDK's complete task level
	// actually becomes empty. parkedAt is a turn boundary and can be much
	// earlier, so using it can retire a process before its final wakeup arrives.
	tasksEmptyAt time.Time
	// lastEventAt advances for every provider stream event, including
	// autonomous wakeups and task-level changes while the bridge is parked.
	lastEventAt time.Time
	parkedAt    time.Time
	dead        bool
	failure     error
	// release frees the account's live-process slot. Held from the moment
	// the bridge first parks until the process is actually gone.
	release func()
	// wake asks the owner to drive a turn nobody prompted. It must not
	// block: it is called from the router, which is the only thing draining
	// the process's stdout.
	wake func() (chan<- agent.StreamEvent, func(error))
	// notice persists a user-visible explanation when the pool has to destroy
	// still-live background work outside a foreground request.
	notice func(string)
	// activity is the owner's view of background work. activityOn records the
	// last state delivered to that callback so provider level snapshots do not
	// produce duplicate global activity broadcasts.
	activity   func(bool)
	activityOn bool
	noticeOnce sync.Once
}

// bridgeSink is one attachment generation. A fresh value is allocated for
// every attach/wakeup so detach may wait for old senders while a later
// generation is installed without reusing a sync.WaitGroup.
type bridgeSink struct {
	ch      chan<- agent.StreamEvent
	done    chan struct{}
	senders sync.WaitGroup
}

func newBridgeSink(ch chan<- agent.StreamEvent) *bridgeSink {
	return &bridgeSink{ch: ch, done: make(chan struct{})}
}

func (b *residentBridge) currentSink() chan<- agent.StreamEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sink == nil {
		return nil
	}
	return b.sink.ch
}

func (b *residentBridge) residentActiveLocked() bool {
	// expectedTurnID is empty for provider-initiated wakeups. A normal prompt
	// is already represented by the foreground Drainer job and must not create
	// a second resident activity merely because it has a sink attached.
	return !b.dead && (b.tasks > 0 || (b.sink != nil && b.expectedTurnID == ""))
}

func (b *residentBridge) syncActivityLocked() (func(bool), bool, bool) {
	active := b.residentActiveLocked()
	if active == b.activityOn {
		return nil, false, false
	}
	b.activityOn = active
	return b.activity, active, true
}

func emitActivity(activity func(bool), active bool, changed bool) {
	if changed && activity != nil {
		activity(active)
	}
}

// attach installs a turn's sink. It returns the channel the turn waits on for
// its terminal state: nil when the turn reached `result`, an error when the
// process died underneath it.
func (b *residentBridge) attach(sink chan<- agent.StreamEvent, expectedTurnID string) (<-chan error, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dead {
		return nil, b.failureLocked()
	}
	if b.sink != nil {
		return nil, errors.New("resident CAS bridge already has a turn attached")
	}
	b.sink = newBridgeSink(sink)
	b.turnEnd = make(chan error, 1)
	b.expectedTurnID = expectedTurnID
	b.parkedAt = time.Time{}
	return b.turnEnd, nil
}

// expectSteeredTurn moves the foreground boundary to a newly queued user
// message. It shares b.mu with resultEndsAttachedTurn, so either the old result
// wins and steering falls back to the durable dispatcher queue, or steering
// wins and the old result cannot detach the sink underneath the new turn.
func (b *residentBridge) expectSteeredTurn(messageID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dead || b.sink == nil || messageID == "" {
		return agent.ErrNoActiveTurn
	}
	b.expectedTurnID = messageID
	return nil
}

// detach removes the current sink and settles the turn's terminal channel. It
// is idempotent so the router and a cancelling turn can both call it.
func (b *residentBridge) detach(cause error) {
	b.mu.Lock()
	sink, turnEnd := b.detachLocked()
	activity, active, changed := b.syncActivityLocked()
	b.mu.Unlock()
	settleDetachedSink(sink, turnEnd, cause)
	emitActivity(activity, active, changed)
}

func (b *residentBridge) detachLocked() (*bridgeSink, chan error) {
	if b.sink == nil {
		return nil, nil
	}
	sink := b.sink
	b.sink = nil
	close(sink.done)
	b.expectedTurnID = ""
	turnEnd := b.turnEnd
	b.turnEnd = nil
	b.parkedAt = nowFunc()
	return sink, turnEnd
}

func settleDetachedSink(sink *bridgeSink, turnEnd chan error, cause error) {
	if sink == nil {
		return
	}
	// Every Add happens while residentBridge.mu still points at this sink.
	// detach removes it under that same lock before reaching Wait, so no new
	// sender can race an Add against this Wait.
	sink.senders.Wait()
	if turnEnd != nil {
		turnEnd <- cause
	}
}

func (b *residentBridge) failureLocked() error {
	if b.failure != nil {
		return b.failure
	}
	return errBridgeRetired
}

// liveTasks reports the current level set size.
func (b *residentBridge) liveTasks() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tasks
}

// clearTasks forces the level set empty. Used after a locally-initiated
// interrupt: cancel_queued is authoritative on our side, and waiting for the
// SDK to confirm with an empty level frame would park a gigabyte for the full
// hard TTL if that frame never comes.
func (b *residentBridge) clearTasks() {
	b.mu.Lock()
	if b.tasks > 0 {
		b.tasksEmptyAt = nowFunc()
	}
	b.tasks = 0
	activity, active, changed := b.syncActivityLocked()
	b.mu.Unlock()
	emitActivity(activity, active, changed)
}

// route consumes the process's whole stdout stream. It is the only writer to
// any turn's sink.
func (b *residentBridge) route() {
	for evt := range b.events {
		b.recordEvent()
		switch evt.Kind {
		case agent.KindBackgroundTasks:
			b.applyTaskLevel(evt)
			// Task membership is bookkeeping, not proof that the provider
			// started a new turn. Forward it to an existing listener for UI
			// state, but never manufacture a wakeup from a trailing empty set.
			b.forward(evt, false)
			continue
		case agent.KindTaskNotification:
			// A cue, not transcript. Forwarded when a turn is listening;
			// dropped rather than treated as the start of a wakeup, since
			// it arrives alongside the level frame that settles a task.
			b.forward(evt, false)
			continue
		}
		b.forward(evt, startsWakeup(evt))
		if evt.Kind == agent.KindResult && !evt.Subagent && b.resultEndsAttachedTurn(evt) {
			b.detach(nil)
		}
	}
	// The stream ended: the process is gone or going. Settle whatever turn
	// was attached rather than leaving it waiting on a stream that will never
	// produce another frame. A clean EOF settles it with nil — that is how a
	// non-resident turn ends, and its exit status is read afterwards.
	b.mu.Lock()
	b.dead = true
	sink, turnEnd := b.detachLocked()
	activity, active, changed := b.syncActivityLocked()
	b.mu.Unlock()
	settleDetachedSink(sink, turnEnd, b.failure)
	emitActivity(activity, active, changed)
}

// startsWakeup reports whether a frame arriving with no turn attached means
// the provider began a turn of its own. Background sub-agents keep streaming
// their work (and their API calls' rate-limit frames) while the parent is
// parked; none of it is the parent reacting. Waking on it opened a turn that
// only a later top-level result could end, so the chat sat on "thinking" — and
// hid the background Stop button — for as long as the sub-agents ran.
func startsWakeup(evt agent.StreamEvent) bool {
	return !evt.Subagent && evt.Kind != agent.KindRateLimit
}

func (b *residentBridge) recordEvent() {
	b.mu.Lock()
	b.lastEventAt = nowFunc()
	b.mu.Unlock()
}

func (b *residentBridge) applyTaskLevel(evt agent.StreamEvent) {
	var payload struct {
		Tasks []struct {
			Ambient bool `json:"ambient"`
		} `json:"tasks"`
	}
	if json.Unmarshal([]byte(evt.Content), &payload) != nil {
		return
	}
	live := 0
	for _, task := range payload.Tasks {
		if !task.Ambient {
			live++
		}
	}
	b.mu.Lock()
	previous := b.tasks
	b.tasks = live
	if b.tasks > 0 {
		b.tasksEmptyAt = time.Time{}
	} else if previous > 0 {
		b.tasksEmptyAt = nowFunc()
	}
	activity, active, changed := b.syncActivityLocked()
	b.mu.Unlock()
	emitActivity(activity, active, changed)
}

// resultEndsAttachedTurn distinguishes a user result from a background result.
// Correlation is strongest when the SDK supplies the turn's consumption list
// (user_message_uuids), and falls back to user_message_uuid alone for older
// producers. The origin fallback keeps current SDK error-result variants safe,
// while the final true preserves compatibility with older SDKs that expose
// neither field.
func (b *residentBridge) resultEndsAttachedTurn(evt agent.StreamEvent) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sink == nil {
		return false
	}
	if b.expectedTurnID == "" {
		return true
	}
	// A background turn can still point at the human message that originally
	// spawned its task. Origin therefore takes precedence over an equal UUID.
	if strings.HasPrefix(evt.TurnOrigin, "task-notification") {
		return false
	}
	if evt.TurnID != "" {
		return b.consumedExpectedTurn(evt)
	}
	return true
}

// consumedExpectedTurn asks whether the turn this result ends is the one the
// attached caller is waiting for. The list is authoritative when present: a
// message steered into a turn already in flight is folded into it, so the
// only result that will ever arrive names the original prompt in TurnID and
// the steered message in TurnIDs. Matching TurnID alone left that caller
// waiting for a second result the provider never produces.
func (b *residentBridge) consumedExpectedTurn(evt agent.StreamEvent) bool {
	for _, id := range evt.TurnIDs {
		if id == b.expectedTurnID {
			return true
		}
	}
	return evt.TurnID == b.expectedTurnID
}

// forward delivers one frame to the attached turn. With no turn attached and
// mayWake set, the frame is the first of a turn the provider started on its
// own — a background task settled and the model reacted — so ask the owner
// for somewhere to put it.
func (b *residentBridge) forward(evt agent.StreamEvent, mayWake bool) {
	b.mu.Lock()
	if b.sink == nil {
		if !mayWake || b.wake == nil || b.dead {
			b.mu.Unlock()
			return
		}
		sink, done := b.wake()
		if sink == nil {
			b.mu.Unlock()
			return
		}
		b.sink = newBridgeSink(sink)
		b.turnEnd = make(chan error, 1)
		b.parkedAt = time.Time{}
		go b.settleWakeup(b.turnEnd, done)
	}
	sink := b.sink
	sink.senders.Add(1)
	activity, active, changed := b.syncActivityLocked()
	b.mu.Unlock()

	// A full or abandoned consumer may block here, but never while holding
	// residentBridge.mu. detach closes done, which releases this send before it
	// waits for the sender and lets the turn safely close its output channel.
	select {
	case sink.ch <- evt:
	case <-sink.done:
	}
	sink.senders.Done()
	emitActivity(activity, active, changed)
}

// settleWakeup reports a provider-initiated turn's terminal state back to its
// owner. A prompted turn's caller waits on the channel itself; a wakeup has no
// caller, so this goroutine stands in for one.
func (b *residentBridge) settleWakeup(turnEnd chan error, done func(error)) {
	err := <-turnEnd
	if done != nil {
		done(err)
	}
}

func (b *residentBridge) isDead() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dead
}

// setWake installs the owner's wakeup sink factory, replacing whatever an
// earlier turn left behind: the newest turn's closure is the one whose
// conversation state is current.
func (b *residentBridge) setWake(wake func() (chan<- agent.StreamEvent, func(error))) {
	b.mu.Lock()
	b.wake = wake
	b.mu.Unlock()
}

func (b *residentBridge) setNotice(notice func(string)) {
	b.mu.Lock()
	b.notice = notice
	b.mu.Unlock()
}

// setActivity transfers the live-state subscription to the newest turn. A
// resident bridge can be reused many times; balancing the old callback before
// activating the new one keeps per-turn registry keys from leaking forever.
func (b *residentBridge) setActivity(activity func(bool)) {
	b.mu.Lock()
	old, oldOn := b.activity, b.activityOn
	active := b.residentActiveLocked()
	b.activity = activity
	b.activityOn = active
	b.mu.Unlock()
	if old != nil && oldOn {
		old(false)
	}
	if activity != nil && active {
		activity(true)
	}
}

// retire is the single path for pool-driven destruction. Foreground teardown
// intentionally uses markDead directly: the foreground caller already owns the
// error. Pool teardown can otherwise happen silently minutes later, so it
// persists one explanation when live work is lost.
func (b *residentBridge) retire(cause error, reason string) error {
	b.mu.Lock()
	notice := b.notice
	hasLiveTasks := b.tasks > 0
	b.mu.Unlock()
	if notice != nil && hasLiveTasks && reason != "" {
		b.noticeOnce.Do(func() {
			notice("Background work in this conversation was stopped because " + reason + ".")
		})
	}
	return b.markDead(cause)
}

func (b *residentBridge) setRelease(release func()) {
	b.mu.Lock()
	b.release = release
	b.mu.Unlock()
}

func (b *residentBridge) hasRelease() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.release != nil
}

func (b *residentBridge) nextTurnID() string {
	return "turn-" + strconv.FormatUint(b.turns.Add(1), 10)
}

// awaitTurn blocks until the turn reaches `result`, the process dies under it,
// or the caller cancels.
//
// Cancellation is where residency changes the meaning of Stop. Signalling the
// process group would take the background tasks with it, so instead the SDK is
// asked to abort the turn and cancel its queued wakeups, leaving the process
// up. The local task set is then forced empty regardless of what the SDK
// reports: cancel_queued is authoritative on our side, and waiting for a
// confirming level frame that may never arrive would park a gigabyte for the
// full hard TTL with nothing left to wait for.
func (b *residentBridge) awaitTurn(ctx context.Context, turnEnd <-chan error) error {
	select {
	case cause := <-turnEnd:
		return cause
	case <-ctx.Done():
	}
	if b.active != nil {
		interruptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), interruptGrace)
		err := b.active.interrupt(interruptCtx, b.nextTurnID())
		cancel()
		if err == nil {
			select {
			case <-turnEnd:
			case <-time.After(interruptGrace):
			}
			b.clearTasks()
			b.detach(ctx.Err())
			return ctx.Err()
		}
		log.Printf("[claude-CAS] interrupt failed for conversation %s, falling back to terminating the process: %v", b.key, err)
	}
	b.clearTasks()
	b.detach(ctx.Err())
	return ctx.Err()
}

// finishTurn tears the process down and folds its exit into the turn's error,
// preserving the pre-residency precedence: a stream failure explains the turn,
// while the exit status only ever describes DayMug's own SIGTERM.
func (b *residentBridge) finishTurn(turnErr error) error {
	waitErr := b.markDead(nil)
	if turnErr != nil {
		return turnErr
	}
	if waitErr != nil {
		if diagnostic := bridgeDiagnostic(b.stderr.String()); diagnostic != "" {
			return fmt.Errorf("%w: %s", waitErr, diagnostic)
		}
		return waitErr
	}
	return nil
}

// markDead retires the process and returns its exit error. Safe to call
// repeatedly and from several goroutines; only the first call does the work.
//
// Closing stdin first is what lets the SDK end its query and close the session
// JSONL cleanly — the file the next --resume reads. The context cancel behind
// it is a backstop for a bridge that ignores the closed stream, not the
// primary path.
func (b *residentBridge) markDead(cause error) error {
	b.teardown.Do(func() {
		b.mu.Lock()
		b.dead = true
		if b.failure == nil && cause != nil {
			b.failure = cause
		}
		release := b.release
		b.release = nil
		activity, active, changed := b.syncActivityLocked()
		b.mu.Unlock()
		emitActivity(activity, active, changed)

		b.unregister()
		exited := make(chan error, 1)
		go func() { exited <- b.proc.Wait() }()
		select {
		case b.exitErr = <-exited:
		case <-time.After(exitGrace):
			b.cancel()
			b.exitErr = <-exited
		}
		b.cancel()
		b.stderrWait()
		if release != nil {
			release()
		}
	})
	return b.exitErr
}

// reclaimable reports whether an idle bridge has outlived its usefulness.
func (b *residentBridge) reclaimable(now time.Time) (bool, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dead {
		return true, "process exited"
	}
	if b.sink != nil || b.parkedAt.IsZero() {
		return false, ""
	}
	if b.tasks == 0 {
		emptySince := b.tasksEmptyAt
		if emptySince.IsZero() {
			emptySince = b.parkedAt
		}
		if now.Sub(emptySince) > parkDrainGrace {
			return true, "background work finished"
		}
	} else {
		lastEventAt := b.lastEventAt
		if lastEventAt.IsZero() {
			lastEventAt = b.parkedAt
		}
		if now.Sub(lastEventAt) >= backgroundEventSilenceTTL {
			return true, fmt.Sprintf("no background event for %s with %d task(s) still live", backgroundEventSilenceTTL, b.tasks)
		}
	}
	if now.Sub(b.parkedAt) > parkHardTTL {
		return true, fmt.Sprintf("parked longer than %s with %d task(s) still live", parkHardTTL, b.tasks)
	}
	return false, ""
}

// Busy and IdleSince make residentBridge a residentpool.Member. A bridge is
// idle from the moment it parks; parkedAt is zero while a turn is attached.
func (b *residentBridge) IdleSince() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.parkedAt
}

func (b *residentBridge) Busy() bool { return b.currentSink() != nil }

// bridgePool holds the parked bridges. Only conversations that left background
// work running are ever in it.
type bridgePool struct {
	mu      sync.Mutex
	bridges map[string]*residentBridge
	closed  bool
	reaper  sync.Once
}

func newBridgePool() *bridgePool {
	return &bridgePool{bridges: make(map[string]*residentBridge)}
}

// take removes and returns a live parked bridge whose fingerprint matches.
// A mismatch retires the stale one and reports why, so the caller can tell the
// user their settings change cost them the background work.
func (p *bridgePool) take(key, fingerprint string) (*residentBridge, string) {
	if p == nil || key == "" {
		return nil, ""
	}
	p.mu.Lock()
	b := p.bridges[key]
	if b != nil {
		delete(p.bridges, key)
	}
	p.mu.Unlock()
	if b == nil {
		return nil, ""
	}
	if b.currentSink() != nil {
		// A turn is already attached — another request for the same
		// conversation is mid-flight. Leave it alone and spawn fresh.
		p.put(b)
		return nil, ""
	}
	if b.fingerprint != fingerprint {
		_ = b.markDead(errors.New("conversation settings changed"))
		return nil, "settings changed for this turn"
	}
	if b.isDead() {
		go func() { _ = b.retire(errors.New("resident SDK process exited"), "the resident SDK process exited") }()
		return nil, ""
	}
	return b, ""
}

// put parks a bridge, enforcing the global cap by retiring the least recently
// used idle entry. Returns false when the pool is shut down.
func (p *bridgePool) put(b *residentBridge) bool {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return false
	}
	p.reclaimLocked(nowFunc(), b.key)
	if existing := p.bridges[b.key]; existing != nil && existing != b {
		delete(p.bridges, b.key)
		go func() {
			_ = existing.retire(errors.New("replaced by a newer bridge for the same conversation"), "a newer resident session replaced it")
		}()
	}
	p.bridges[b.key] = b
	// Victims are retired off-lock: markDead waits up to exitGrace for the
	// process, and holding p.mu through that would stall every conversation.
	evicted := residentpool.EvictOverCap(p.bridges, maxParkedBridges, b.key)
	p.mu.Unlock()
	for _, victim := range evicted {
		log.Printf("[claude-CAS] resident cap %d reached, retiring conversation %s and its background work", maxParkedBridges, victim.key)
		go func(victim *residentBridge) {
			_ = victim.retire(errBridgeRetired, "the resident-session limit was reached")
		}(victim)
	}
	p.startReaper()
	return true
}

// reclaimLocked drops dead and expired bridges. keep is exempt: it is the
// conversation the caller is about to use.
func (p *bridgePool) reclaimLocked(now time.Time, keep string) {
	for key, b := range p.bridges {
		if key == keep {
			continue
		}
		if ok, why := b.reclaimable(now); ok {
			delete(p.bridges, key)
			log.Printf("[claude-CAS] retiring resident bridge for conversation %s: %s", key, why)
			reason := ""
			switch {
			case why == "process exited":
				reason = "the resident SDK process exited"
			case strings.HasPrefix(why, "no background event for"):
				reason = "it produced no events for 30 minutes"
			case strings.HasPrefix(why, "parked longer than"):
				reason = "it was still running after 30 minutes"
			}
			go func(b *residentBridge, reason string) { _ = b.retire(errBridgeRetired, reason) }(b, reason)
		}
	}
}

// stop retires the parked bridge for key at the user's request. A bridge with
// a turn attached is not parked and is left to that turn's own cancel.
func (p *bridgePool) stop(key string) bool {
	if p == nil || key == "" {
		return false
	}
	p.mu.Lock()
	b := p.bridges[key]
	if b == nil || b.Busy() {
		p.mu.Unlock()
		return false
	}
	delete(p.bridges, key)
	p.mu.Unlock()
	log.Printf("[claude-CAS] stopping resident bridge for conversation %s at the user's request", key)
	go func() { _ = b.retire(errBridgeRetired, "you stopped it") }()
	return true
}

func (p *bridgePool) startReaper() {
	p.reaper.Do(func() {
		go func() {
			ticker := time.NewTicker(reaperInterval)
			defer ticker.Stop()
			for range ticker.C {
				p.mu.Lock()
				closed := p.closed
				if !closed {
					p.reclaimLocked(nowFunc(), "")
				}
				p.mu.Unlock()
				if closed {
					return
				}
			}
		}()
	})
}

// closeAll retires the whole fleet. Registered with agent.RegisterCloser so a
// clean shutdown reaps parked bridges: they hold no drainer job, so the job
// drain that precedes shutdown would otherwise finish without touching them
// and leave one orphan per parked conversation.
func (p *bridgePool) closeAll() {
	p.mu.Lock()
	p.closed = true
	bridges := p.bridges
	p.bridges = make(map[string]*residentBridge)
	p.mu.Unlock()
	var wg sync.WaitGroup
	for _, b := range bridges {
		wg.Add(1)
		go func(b *residentBridge) {
			defer wg.Done()
			_ = b.markDead(errPoolClosed)
		}(b)
	}
	wg.Wait()
}

func (p *bridgePool) parkedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bridges)
}

// parkBlocksUpgradeFor bounds how long a parked bridge may postpone an
// opportunistic restart. Background work that has been running for a quarter
// of an hour is almost certainly a real job worth waiting for; past that,
// "waiting for background work" becomes indistinguishable from a wedged
// bridge holding upgrades hostage forever.
const parkBlocksUpgradeFor = 15 * time.Minute

// activeResidentCount reports bridges whose background work is recent enough
// that restarting the server would visibly destroy something. It is what the
// drainer consults before a graceful upgrade or scheduled restart decides the
// server is idle — parked bridges hold no drainer job, so without this they
// are invisible to exactly the code path that would kill them.
func (p *bridgePool) activeResidentCount() int {
	if p == nil {
		return 0
	}
	now := nowFunc()
	p.mu.Lock()
	defer p.mu.Unlock()
	count := 0
	for _, b := range p.bridges {
		parkedAt := b.IdleSince()
		if b.liveTasks() == 0 || parkedAt.IsZero() {
			continue
		}
		if now.Sub(parkedAt) < parkBlocksUpgradeFor {
			count++
		}
	}
	return count
}

// bridgeFingerprint captures everything the SDK bakes in when query() is
// called. Isolation inputs mirror codexapp.runtimeKey — a pooled process must
// never be handed to a request whose sandboxing differs — plus the request
// fields buildOptions reads exactly once, which a parked process cannot be
// told about afterwards.
func bridgeFingerprint(opts agent.RunRequest, req bridgeRequest) string {
	h := sha256.New()
	write := func(parts ...string) {
		for _, part := range parts {
			_, _ = io.WriteString(h, part)
			_, _ = h.Write([]byte{0})
		}
	}
	write(opts.ConfigDir, opts.JailRoot, fmt.Sprintf("%T", opts.Sandbox), fmt.Sprintf("%T", effectiveSpawner(opts)))
	write(strconv.FormatBool(opts.Unrestricted))
	keys := make([]string, 0, len(opts.AccountEnv))
	for k := range opts.AccountEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		write(k, opts.AccountEnv[k])
	}
	write(req.CWD, req.SessionID, req.Model, req.Effort, req.SystemPrompt)
	write(strconv.FormatBool(req.ReadOnly), strconv.FormatBool(req.EnableUserQuestions), strconv.FormatBool(req.Minimal))
	if mcp, err := json.Marshal(req.MCPServers); err == nil {
		write(string(mcp))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// newResidentBridge spawns a process and starts pumping its stdout. The reader
// and router goroutines live for the process, not the turn.
func (r *runner) newResidentBridge(ctx context.Context, req bridgeRequest, workDir string, opts agent.RunRequest, fingerprint string) (*residentBridge, error) {
	// A resident process must outlive the turn that spawned it, so it cannot
	// hang off the turn's context: cancelling that context is how DayMug
	// signals the process group, and doing so at every turn boundary would
	// kill the background work the whole feature exists to keep. Turn
	// cancellation reaches a resident bridge through interrupt instead
	// (awaitTurn), and the process is torn down explicitly by markDead.
	procCtx := ctx
	if req.Resident {
		procCtx = context.WithoutCancel(ctx)
	}
	proc, cancel, err := r.spawnBridge(procCtx, req, workDir, opts)
	if err != nil {
		return nil, err
	}
	stderr, stderrWait := streamcommon.DrainStderr(proc.Stderr())
	var active *activeBridge
	if stdin := proc.Stdin(); opts.ControlID != "" && stdin != nil {
		active = newActiveBridge(stdin)
	}
	b := &residentBridge{
		key:         opts.ControlID,
		fingerprint: fingerprint,
		proc:        proc,
		active:      active,
		cancel:      cancel,
		stderr:      stderr,
		stderrWait:  stderrWait,
		events:      make(chan agent.StreamEvent, 64),
		unregister:  func() { r.unregisterActiveBridge(opts.ControlID, active) },
	}
	if active != nil {
		active.beforeSteer = b.expectSteeredTurn
		r.registerActiveBridge(opts.ControlID, active)
	}
	go b.route()
	go func() {
		defer close(b.events)
		cfg := r.lineStream(b, workDir, opts)
		if err := streamcommon.StreamLines(procCtx, proc.Stdout(), b.events, cfg); err != nil {
			b.mu.Lock()
			if b.failure == nil {
				b.failure = err
			}
			b.mu.Unlock()
		}
	}()
	return b, nil
}

// lineStream builds the per-process read configuration. StallExempt is what
// lets a parked bridge sit quiet: between turns nobody is waiting on the
// stream, so silence is the expected state rather than a hung upstream.
func (r *runner) lineStream(b *residentBridge, workDir string, opts agent.RunRequest) streamcommon.LineStream {
	processor := claudecli.NewStreamProcessorForRun("claude", opts.Model)
	return streamcommon.LineStream{
		CLIName:          "CAS",
		Process:          r.frameProcessor(b, processor),
		StallTimeout:     opts.StallTimeout,
		MaxSilentTimeout: opts.MaxSilentTimeout,
		StallExempt:      func() bool { return b.currentSink() == nil },
		ActivityProbe: agent.NewProcessActivityProbe(b.proc, func() []string {
			if opts.SessionID == "" {
				return nil
			}
			return []string{r.SessionLogPath(workDir, opts.SessionID, opts.ConfigDir)}
		}),
	}
}
