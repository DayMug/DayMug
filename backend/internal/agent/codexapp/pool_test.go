package codexapp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// flakySpawner fails its first `fails` spawns after the gate opens, then
// delegates. One type for both outcomes, because runtimeKey fingerprints the
// spawner's type and a retry must land on the same key as the failure.
type flakySpawner struct {
	inner   *scriptedSpawner
	entered chan struct{}
	gate    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	fails   int
}

func (f *flakySpawner) Spawn(ctx context.Context, req agent.SpawnRequest) (agent.RunningProcess, error) {
	f.once.Do(func() { close(f.entered) })
	<-f.gate
	f.mu.Lock()
	fail := f.fails > 0
	if fail {
		f.fails--
	}
	f.mu.Unlock()
	if fail {
		return nil, errors.New("spawn exploded")
	}
	return f.inner.Spawn(ctx, req)
}

func spawnCount(s *scriptedSpawner) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spawnCount
}

func pooledServerCount(p *serverPool) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.servers)
}

func TestConcurrentLookupsForOneKeySpawnOnce(t *testing.T) {
	pool := newServerPool()
	defer pool.closeAll()
	inner := &scriptedSpawner{}
	gated := &gatedSpawner{inner: inner, entered: make(chan struct{}), gate: make(chan struct{})}
	opts := agent.RunRequest{ConfigDir: "/account", Spawner: gated}

	type result struct {
		srv *accountServer
		err error
	}
	first := make(chan result, 1)
	go func() {
		srv, release, err := pool.get(context.Background(), opts, "/workspace")
		if err == nil {
			release()
		}
		first <- result{srv, err}
	}()
	<-gated.entered
	second := make(chan result, 1)
	go func() {
		srv, release, err := pool.get(context.Background(), opts, "/other-workspace")
		if err == nil {
			release()
		}
		second <- result{srv, err}
	}()
	select {
	case <-second:
		t.Fatal("a second lookup for a key being spawned did not wait for that spawn")
	case <-time.After(50 * time.Millisecond):
	}
	close(gated.gate)
	a, b := <-first, <-second
	if a.err != nil || b.err != nil {
		t.Fatalf("errors = %v / %v", a.err, b.err)
	}
	if a.srv != b.srv {
		t.Fatal("two lookups for one key got different processes")
	}
	if n := spawnCount(inner); n != 1 {
		t.Fatalf("spawn count = %d, want 1 process per key", n)
	}
}

func TestFailedColdStartReachesWaitersAndIsRetried(t *testing.T) {
	pool := newServerPool()
	defer pool.closeAll()
	// Two failures: whether the second caller parks on the first spawn (the
	// path under test) or loses the race and spawns itself, it must fail.
	flaky := &flakySpawner{inner: &scriptedSpawner{}, entered: make(chan struct{}), gate: make(chan struct{}), fails: 2}
	opts := agent.RunRequest{ConfigDir: "/account", Spawner: flaky}

	firstErr := make(chan error, 1)
	go func() {
		_, _, err := pool.get(context.Background(), opts, "/workspace")
		firstErr <- err
	}()
	<-flaky.entered
	waiterErr := make(chan error, 1)
	go func() {
		_, _, err := pool.get(context.Background(), opts, "/workspace")
		waiterErr <- err
	}()
	// Let the waiter park on the pending start before the spawn resolves.
	time.Sleep(50 * time.Millisecond)
	close(flaky.gate)
	if err := <-firstErr; err == nil {
		t.Fatal("spawner failure was swallowed")
	}
	if err := <-waiterErr; err == nil {
		t.Fatal("a waiter on a failed spawn reported success")
	}
	flaky.mu.Lock()
	flaky.fails = 0
	flaky.mu.Unlock()

	srv, release, err := pool.get(context.Background(), opts, "/workspace")
	if err != nil {
		t.Fatalf("lookup after a failed spawn = %v, want a fresh attempt", err)
	}
	release()
	if !srv.alive() {
		t.Fatal("retried spawn handed out a dead server")
	}
}

func TestWaiterCancelledDuringColdStartReturnsContextError(t *testing.T) {
	pool := newServerPool()
	defer pool.closeAll()
	gated := &gatedSpawner{inner: &scriptedSpawner{}, entered: make(chan struct{}), gate: make(chan struct{})}
	opts := agent.RunRequest{ConfigDir: "/account", Spawner: gated}

	spawnerDone := make(chan error, 1)
	go func() {
		_, release, err := pool.get(context.Background(), opts, "/workspace")
		if err == nil {
			release()
		}
		spawnerDone <- err
	}()
	<-gated.entered

	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, _, err := pool.get(ctx, opts, "/workspace")
		waiterDone <- err
	}()
	cancel()
	select {
	case err := <-waiterDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled waiter stayed parked behind someone else's cold start")
	}
	close(gated.gate)
	if err := <-spawnerDone; err != nil {
		t.Fatalf("the spawning caller was affected by a waiter's cancel: %v", err)
	}
}

func TestLookupAfterCloseAllIsRefused(t *testing.T) {
	pool := newServerPool()
	pool.closeAll()
	if _, _, err := pool.get(context.Background(), agent.RunRequest{ConfigDir: "/account", Spawner: &scriptedSpawner{}}, "/workspace"); !errors.Is(err, errPoolClosed) {
		t.Fatalf("get after closeAll = %v, want errPoolClosed", err)
	}
}

func TestCloseAllDuringColdStartDiscardsTheNewServer(t *testing.T) {
	pool := newServerPool()
	inner := &scriptedSpawner{}
	gated := &gatedSpawner{inner: inner, entered: make(chan struct{}), gate: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, _, err := pool.get(context.Background(), agent.RunRequest{ConfigDir: "/account", Spawner: gated}, "/workspace")
		done <- err
	}()
	<-gated.entered
	pool.closeAll()
	close(gated.gate)
	if err := <-done; !errors.Is(err, errPoolClosed) {
		t.Fatalf("spawn racing shutdown = %v, want errPoolClosed", err)
	}
	if n := spawnCount(inner); n != 1 {
		t.Fatalf("spawn count = %d, want the in-flight spawn to have run", n)
	}
	if n := pooledServerCount(pool); n != 0 {
		t.Fatalf("a server spawned during shutdown was published (%d pooled)", n)
	}
}

// The cap evicts the least recently idle server, but never the one the caller
// is about to use and never one with a turn in flight.
func TestFleetOverCapEvictsLeastRecentlyIdle(t *testing.T) {
	clock := time.Now()
	nowFunc = func() time.Time { return clock }
	t.Cleanup(func() { nowFunc = time.Now })

	pool := newServerPool()
	defer pool.closeAll()
	spawner := &scriptedSpawner{}
	optsFor := func(i int) agent.RunRequest {
		return agent.RunRequest{ConfigDir: fmt.Sprintf("/account-%d", i), Spawner: spawner}
	}
	servers := make([]*accountServer, maxPooledServers+1)
	for i := range servers {
		srv, release, err := pool.get(context.Background(), optsFor(i), "/workspace")
		if err != nil {
			t.Fatal(err)
		}
		servers[i] = srv
		// Server 0 is the oldest and keeps a turn in flight.
		if i != 0 {
			release()
		}
		clock = clock.Add(time.Second)
	}
	if n := pooledServerCount(pool); n != maxPooledServers+1 {
		t.Fatalf("pooled = %d, want the cap to be enforced lazily on the next lookup", n)
	}

	// Server 1 is now the least recently idle, but it is the lookup's key.
	again, release, err := pool.get(context.Background(), optsFor(1), "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if again != servers[1] {
		t.Fatal("the key being looked up was evicted and respawned")
	}
	if !servers[0].alive() {
		t.Fatal("a server with a turn in flight was evicted to honour the cap")
	}
	if servers[2].alive() {
		t.Fatal("the least recently idle eligible server survived an over-cap lookup")
	}
	if !errors.Is(servers[2].failure(), errServerReclaimed) {
		t.Fatalf("evicted server failure = %v, want errServerReclaimed", servers[2].failure())
	}
	for i := 3; i < len(servers); i++ {
		if !servers[i].alive() {
			t.Fatalf("server %d was evicted; only one was over the cap", i)
		}
	}
	if n := pooledServerCount(pool); n != maxPooledServers {
		t.Fatalf("pooled = %d, want %d", n, maxPooledServers)
	}
}

func TestFleetOfBusyServersMayExceedTheCap(t *testing.T) {
	pool := newServerPool()
	defer pool.closeAll()
	spawner := &scriptedSpawner{}
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for i := 0; i < maxPooledServers+2; i++ {
		_, release, err := pool.get(context.Background(), agent.RunRequest{ConfigDir: fmt.Sprintf("/busy-%d", i), Spawner: spawner}, "/workspace")
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if n := pooledServerCount(pool); n != maxPooledServers+2 {
		t.Fatalf("pooled = %d, want every busy server kept", n)
	}
}

func TestInvalidateOnlyDropsTheServerItNames(t *testing.T) {
	pool := newServerPool()
	defer pool.closeAll()
	opts := agent.RunRequest{ConfigDir: "/account", Spawner: &scriptedSpawner{}}
	first, release, err := pool.get(context.Background(), opts, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	release()
	cause := errors.New("transport broke")
	pool.invalidate(opts, first, cause)
	if first.alive() || !errors.Is(first.failure(), cause) {
		t.Fatalf("invalidated server alive=%v failure=%v", first.alive(), first.failure())
	}

	second, release, err := pool.get(context.Background(), opts, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if second == first {
		t.Fatal("an invalidated server was handed out again")
	}
	// A late invalidate from a turn that still held the old pointer.
	pool.invalidate(opts, first, cause)
	third, release, err := pool.get(context.Background(), opts, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if third != second || !second.alive() {
		t.Fatal("a stale invalidate evicted the replacement server")
	}
}

// Concurrency shape of the production callers: many turns looking up,
// releasing and invalidating a handful of keys while shutdown lands. Run under
// -race; the assertions are the invariants shutdown must leave behind.
func TestPoolSurvivesConcurrentUseAndShutdown(t *testing.T) {
	pool := newServerPool()
	spawner := &scriptedSpawner{}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				opts := agent.RunRequest{ConfigDir: fmt.Sprintf("/account-%d", (g+i)%3), Spawner: spawner}
				srv, release, err := pool.get(context.Background(), opts, "/workspace")
				if err != nil {
					if !errors.Is(err, errPoolClosed) {
						t.Errorf("get: %v", err)
					}
					return
				}
				if i%5 == 0 {
					pool.invalidate(opts, srv, errors.New("simulated transport failure"))
				}
				release()
			}
		}(g)
	}
	time.Sleep(5 * time.Millisecond)
	pool.closeAll()
	wg.Wait()
	if n := pooledServerCount(pool); n != 0 {
		t.Fatalf("pooled after shutdown = %d, want 0", n)
	}
	if _, _, err := pool.get(context.Background(), agent.RunRequest{ConfigDir: "/account-0", Spawner: spawner}, "/workspace"); !errors.Is(err, errPoolClosed) {
		t.Fatalf("get after shutdown = %v, want errPoolClosed", err)
	}
}
