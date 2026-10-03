package codexapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/residentpool"
	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
)

const (
	// idleServerTTL is how long a pooled app-server may sit with no turn in
	// flight before the next pool lookup reclaims it. Long enough that a user
	// thinking between two messages doesn't pay process startup again, short
	// enough that a day of conversations doesn't leave one resident Codex
	// process per work directory.
	//
	// Three minutes rather than ten because jailed runs still need one process
	// per work dir. Unjailed runs share one process per account, so their warm
	// fleet is normally much smaller.
	idleServerTTL = 3 * time.Minute
	// maxPooledServers bounds the resident fleet. Active process isolation adds
	// the jail root to runtimeKey, so a busy jailed deployment can still grow
	// with its work dirs. Servers with a
	// turn in flight are never evicted, so this is a target rather than a hard
	// ceiling: breaking a running conversation to honour a number is worse
	// than one extra process.
	maxPooledServers = 16
	// subscriptionBuffer is the per-thread notification queue depth. Codex
	// emits a frame per delta, so a slow consumer can fall a long way behind;
	// the queue absorbs bursts and an overflow fails that one turn instead of
	// blocking the dispatcher (see subscription.deliver).
	subscriptionBuffer = 256
)

// nowFunc is the package clock seam so idle-reclamation tests don't sleep.
var nowFunc = time.Now

var (
	errServerReclaimed      = errors.New("codex CAS reclaimed while idle")
	errEventBacklogOverflow = errors.New("codex CAS event backlog overflowed")
	errPoolClosed           = errors.New("codex CAS pool is shut down")
	// errStreamRetry marks an error notification the server flagged as
	// retryable. It is a progress report, not a turn outcome: pumpTurn logs it
	// and keeps pumping.
	errStreamRetry = errors.New("codex CAS stream retry")
)

// subscription is one turn's view of a thread's notification stream.
//
// deliver never blocks: a full queue marks the subscription overflowed and
// closes lost, which fails that one turn. The dispatcher is shared by every
// thread on the account, so blocking it — the previous behaviour, a send with
// only the server's death as an escape — meant one abandoned turn could
// silence every other conversation on the same Codex process indefinitely.
type subscription struct {
	ch   chan wireMessage
	lost chan struct{}
	once sync.Once
}

func newSubscription() *subscription {
	return &subscription{ch: make(chan wireMessage, subscriptionBuffer), lost: make(chan struct{})}
}

func (s *subscription) deliver(msg wireMessage) {
	select {
	case s.ch <- msg:
	default:
		s.drop()
	}
}

func (s *subscription) drop() { s.once.Do(func() { close(s.lost) }) }

type serverPool struct {
	mu      sync.Mutex
	servers map[string]*accountServer
	// starting holds one entry per runtime key whose server is being spawned
	// right now. It is what lets get run startAccountServer outside p.mu while
	// still guaranteeing one process per key.
	starting map[string]*pendingStart
	closed   bool
	// provider is the DayMug CLIType this pool's servers run for; it decides
	// whether an account's API endpoint is wired in as a Codex model provider.
	provider string
}

// pendingStart is one in-flight startAccountServer call. Waiters block on done;
// err is written before done closes, so it is safe to read afterwards.
type pendingStart struct {
	done chan struct{}
	err  error
}

func newServerPool() *serverPool {
	return &serverPool{servers: make(map[string]*accountServer), starting: make(map[string]*pendingStart)}
}

// get returns a live server for the request's runtime key together with the
// release of the turn slot it has already acquired on the caller's behalf.
// Acquiring under the pool lock closes the window in which reclaimLocked could
// see a freshly handed-out server as idle and retire it before the caller
// marked it busy.
//
// The spawn itself runs outside the lock: startAccountServer blocks on process
// startup plus a 15-second initialize round-trip, and holding the pool-wide
// mutex through that froze every other conversation — including ones whose
// server was already warm. A per-key pendingStart preserves the one process
// per key invariant instead: concurrent turns for the same key wait on the
// first spawner, everyone else is untouched.
func (p *serverPool) get(ctx context.Context, opts agent.RunRequest, workDir string) (*accountServer, func(), error) {
	key := runtimeKey(opts)
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, nil, errPoolClosed
		}
		p.reclaimLocked(key)
		if srv := p.servers[key]; srv != nil && srv.alive() {
			release := srv.acquire()
			srv.touch()
			p.mu.Unlock()
			return srv, release, nil
		}
		pending := p.starting[key]
		if pending == nil {
			pending = &pendingStart{done: make(chan struct{})}
			p.starting[key] = pending
			p.mu.Unlock()
			srv, release, err := p.start(ctx, key, opts, workDir)
			pending.err = err
			close(pending.done)
			return srv, release, err
		}
		p.mu.Unlock()
		select {
		case <-pending.done:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
		if pending.err != nil {
			return nil, nil, pending.err
		}
		// Loop: the freshly started server is in the map now.
	}
}

// start spawns the key's server and, on success, publishes it with a turn slot
// already held for the caller — one critical section, so the new server can't
// be reclaimed between publish and acquire.
func (p *serverPool) start(ctx context.Context, key string, opts agent.RunRequest, workDir string) (*accountServer, func(), error) {
	srv, err := startAccountServer(ctx, opts, workDir, compatibleProviderArgs(p.provider, opts.AccountEnv))
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.starting, key)
	if err != nil {
		return nil, nil, err
	}
	if p.closed {
		// closeAll ran while this spawn was in flight; a server published now
		// would outlive the shutdown that was supposed to reap it.
		srv.markDead(errServerReclaimed)
		return nil, nil, errPoolClosed
	}
	p.servers[key] = srv
	release := srv.acquire()
	srv.touch()
	return srv, release, nil
}

// reclaimLocked drops servers that died, then those that have been idle past
// idleServerTTL, then — only if the fleet is still over budget — the
// least-recently-used idle server. `keep` is exempt from the idle passes: it is
// the key the caller is about to use, and killing a warm process just to
// respawn it immediately is pure latency.
func (p *serverPool) reclaimLocked(keep string) {
	now := nowFunc()
	for key, srv := range p.servers {
		switch {
		case !srv.alive():
			delete(p.servers, key)
		case key == keep || srv.Busy():
		case now.Sub(srv.IdleSince()) > idleServerTTL:
			delete(p.servers, key)
			srv.markDead(errServerReclaimed)
		}
	}
	// markDead never blocks (cancel, close, wake subscribers), so unlike the
	// Claude bridge pool the victims can be retired without dropping p.mu.
	for _, srv := range residentpool.EvictOverCap(p.servers, maxPooledServers, keep) {
		srv.markDead(errServerReclaimed)
	}
}

func (p *serverPool) invalidate(opts agent.RunRequest, srv *accountServer, cause error) {
	key := runtimeKey(opts)
	p.mu.Lock()
	if p.servers[key] == srv {
		delete(p.servers, key)
	}
	p.mu.Unlock()
	srv.markDead(cause)
}

// closeAll tears the whole fleet down. Registered with agent.RegisterCloser so
// a clean shutdown reaps these processes: they belong to no job, so the job
// drain never waits for them and agent.TerminateAll only runs when that drain
// times out — without this hook a normal restart or self-upgrade orphaned one
// Codex process per pool entry, each still holding the account's session state.
func (p *serverPool) closeAll() {
	p.mu.Lock()
	p.closed = true
	servers := make([]*accountServer, 0, len(p.servers))
	for key, srv := range p.servers {
		servers = append(servers, srv)
		delete(p.servers, key)
	}
	p.mu.Unlock()
	for _, srv := range servers {
		srv.markDead(errServerReclaimed)
	}
}

// runtimeKey identifies one interchangeable app-server process.
//
// CODEX_HOME identifies the account. Environment and active outer-isolation
// fields are included because they are immutable process-level state and must
// never leak across users that happen to share an account binding. JailRoot is
// deliberately ignored when no outer sandbox is active: app-server receives
// cwd on every thread and turn, so unjailed work directories can safely share
// the account process. The spawner is
// fingerprinted through effectiveSpawner, not opts.Spawner, so a pty-mode
// request — which this adapter downgrades to pipes — shares the pool with the
// pipe-mode requests it is now identical to.
func runtimeKey(opts agent.RunRequest) string {
	h := sha256.New()
	_, _ = io.WriteString(h, opts.ConfigDir+"\x00")
	keys := make([]string, 0, len(opts.AccountEnv))
	for k := range opts.AccountEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = io.WriteString(h, k+"="+opts.AccountEnv[k]+"\x00")
	}
	_, _ = fmt.Fprintf(h, "%T\x00", effectiveSpawner(opts))
	if agent.SandboxIsolatesProcess(opts.Sandbox, opts.Unrestricted) {
		_, _ = fmt.Fprintf(h, "%T\x00%s", opts.Sandbox, opts.JailRoot)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// effectiveSpawner picks the process layout the app-server can actually speak.
//
// Ordinary CLI adapters use a PTY, but it is fatal here. A pty is one
// bidirectional stream with line discipline attached, so the server's
// notifications arrive interleaved with the terminal's echo of our own
// JSON-RPC requests (which then parse as inbound requests and get answered with
// -32601), and every line gains a CR the decoder has to guess about. JSON-RPC
// wants two clean pipes, always.
func effectiveSpawner(opts agent.RunRequest) agent.ProcessSpawner {
	if _, isPty := opts.Spawner.(*agent.PtySpawner); isPty || opts.Spawner == nil {
		return agent.PipeSpawner{}
	}
	return opts.Spawner
}

type accountServer struct {
	client *rpcClient
	proc   agent.RunningProcess
	cancel context.CancelFunc
	mu     sync.Mutex
	subs   map[string]map[*subscription]struct{}
	dead   bool
	err    error
	done   chan struct{}
	// active counts turns in flight; idleAt is when it last dropped to zero.
	active atomic.Int64
	idleAt atomic.Int64
	// threadTotals remembers the last cumulative token count seen per thread,
	// which is how a turn tells a fresh usage report from a replay of the
	// previous turn's numbers (see usage.go). It lives on the server rather
	// than the runner so the memory is reclaimed with the process that owns
	// those threads instead of growing for the life of the daemon.
	threadTotals map[string]int
}

func startAccountServer(ctx context.Context, opts agent.RunRequest, workDir string, extraArgs []string) (*accountServer, error) {
	argv := append([]string{agent.ResolveAgentBinary(codexBinary), "app-server", "--listen", "stdio://"}, extraArgs...)
	extraEnv := []string(nil)
	if opts.Sandbox != nil {
		var err error
		argv, extraEnv, err = opts.Sandbox.Wrap(argv, workDir, agent.WrapOpts{
			ExtraBinds: opts.ExtraBinds, Unrestricted: opts.Unrestricted, JailRoot: opts.JailRoot, ConfigDir: opts.ConfigDir,
		})
		if err != nil {
			return nil, fmt.Errorf("sandbox wrap: %w", err)
		}
	}
	procCtx, cancel := context.WithCancel(context.Background())
	spawner := effectiveSpawner(opts)
	spawnOpts := opts
	spawnOpts.Spawner = spawner
	proc, err := agent.SpawnWithRetry(procCtx, spawner, agent.SpawnRequest{
		Argv: argv, Env: streamcommon.RunEnv(spawnOpts, extraEnv, "CODEX_HOME"), WorkDir: workDir, Interactive: true,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start CAS: %w; install or upgrade Codex with `npm install -g @openai/codex`, then verify `codex app-server --help`", err)
	}
	client := newClient(proc.Stdin(), proc.Stdout())
	srv := &accountServer{client: client, proc: proc, cancel: cancel, subs: make(map[string]map[*subscription]struct{}), done: make(chan struct{}), threadTotals: make(map[string]int)}
	srv.touch()
	client.start()
	go srv.dispatch()
	go func() {
		stderr, waitStderr := streamcommon.DrainStderr(proc.Stderr())
		waitErr := proc.Wait()
		waitStderr()
		if waitErr != nil {
			srv.markDead(fmt.Errorf("codex CAS exited: %w: %s", waitErr, stderr.String()))
		} else {
			srv.markDead(io.ErrUnexpectedEOF)
		}
	}()
	initCtx, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	if err := client.call(initCtx, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "daymug", "title": "DayMug", "version": "1"},
		"capabilities": map[string]any{
			"experimentalApi":           true,
			"optOutNotificationMethods": []string{"thread/started"},
		},
	}, nil); err != nil {
		cancel()
		return nil, fmt.Errorf("initialize CAS: %w; verify this Codex version supports `codex app-server --help` (upgrade with `npm install -g @openai/codex`)", err)
	}
	if err := client.notify("initialized", map[string]any{}); err != nil {
		cancel()
		return nil, err
	}
	return srv, nil
}

func (s *accountServer) dispatch() {
	for msg := range s.client.incoming {
		if len(msg.ID) > 0 && msg.Method != "" {
			if msg.Method != "item/tool/requestUserInput" {
				_ = s.client.respondError(msg.ID, -32601, "unsupported CAS request")
				continue
			}
		}
		threadID := messageThreadID(msg.Params)
		if threadID == "" {
			continue
		}
		s.mu.Lock()
		targets := make([]*subscription, 0, len(s.subs[threadID]))
		for sub := range s.subs[threadID] {
			targets = append(targets, sub)
		}
		s.mu.Unlock()
		if len(targets) == 0 && len(msg.ID) > 0 {
			_ = s.client.respondError(msg.ID, -32602, "no active DayMug turn for request")
			continue
		}
		for _, sub := range targets {
			sub.deliver(msg)
		}
	}
	s.markDead(s.client.readError())
}

func messageThreadID(raw json.RawMessage) string {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.ThreadID
}

func (s *accountServer) subscribe(threadID string) (*subscription, func()) {
	sub := newSubscription()
	s.mu.Lock()
	if s.dead {
		s.mu.Unlock()
		sub.drop()
		return sub, func() {}
	}
	if s.subs[threadID] == nil {
		s.subs[threadID] = make(map[*subscription]struct{})
	}
	s.subs[threadID][sub] = struct{}{}
	s.mu.Unlock()
	return sub, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if group := s.subs[threadID]; group != nil {
			delete(group, sub)
			if len(group) == 0 {
				delete(s.subs, threadID)
			}
		}
	}
}

// acquire marks a turn in flight and returns the release function. Only an
// idle server is reclaimable, so this is what keeps a long turn's process
// alive past idleServerTTL.
func (s *accountServer) acquire() func() {
	s.active.Add(1)
	return func() {
		if s.active.Add(-1) <= 0 {
			s.touch()
		}
	}
}

// Busy and IdleSince make accountServer a residentpool.Member.
func (s *accountServer) Busy() bool { return s.active.Load() > 0 }
func (s *accountServer) touch()     { s.idleAt.Store(nowFunc().UnixNano()) }
func (s *accountServer) IdleSince() time.Time {
	return time.Unix(0, s.idleAt.Load())
}

func (s *accountServer) markDead(err error) {
	s.mu.Lock()
	if s.dead {
		s.mu.Unlock()
		return
	}
	s.dead = true
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	s.err = err
	subs := s.subs
	s.subs = make(map[string]map[*subscription]struct{})
	s.mu.Unlock()
	s.cancel()
	close(s.done)
	// Wake every parked turn: the run loop selects on done, but a turn that is
	// mid-drain of its own queue should not have to wait for a timer.
	for _, group := range subs {
		for sub := range group {
			sub.drop()
		}
	}
	if !errors.Is(err, errServerReclaimed) {
		log.Printf("[codex-CAS] server retired: %v", err)
	}
}

func (s *accountServer) alive() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.dead }

func (s *accountServer) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	return io.ErrUnexpectedEOF
}
