package claudeagentsdk

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// exitedProcess stands in for a bridge whose process has already gone, so
// markDead can run its whole teardown without a real child.
type exitedProcess struct{}

func (exitedProcess) Stdout() io.Reader        { return strings.NewReader("") }
func (exitedProcess) Stderr() io.Reader        { return nil }
func (exitedProcess) Stdin() io.WriteCloser    { return nil }
func (exitedProcess) Wait() error              { return nil }
func (exitedProcess) Signal(_ os.Signal) error { return nil }

// fakeBridge is a parked bridge with live background work (so reclaim leaves
// it alone) whose release counts into released.
func fakeBridge(key string, parkedAt time.Time, released *atomic.Int32) *residentBridge {
	return &residentBridge{
		key:         key,
		fingerprint: "fp",
		proc:        exitedProcess{},
		cancel:      func() {},
		stderr:      &strings.Builder{},
		stderrWait:  func() {},
		unregister:  func() {},
		tasks:       1,
		parkedAt:    parkedAt,
		lastEventAt: parkedAt,
		release:     func() { released.Add(1) },
	}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func pinClock(t *testing.T, at time.Time) {
	t.Helper()
	restore := nowFunc
	nowFunc = func() time.Time { return at }
	t.Cleanup(func() { nowFunc = restore })
}

func setMaxParked(t *testing.T, n int) {
	t.Helper()
	restore := maxParkedBridges
	maxParkedBridges = n
	t.Cleanup(func() { maxParkedBridges = restore })
}

func (p *bridgePool) parked(key string) *residentBridge {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bridges[key]
}

// The cap retires the least recently parked bridge, but never the one being
// parked and never one with a turn attached.
func TestPutOverCapRetiresLeastRecentlyParked(t *testing.T) {
	base := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	pinClock(t, base)
	setMaxParked(t, 3)
	var busyReleased, oldReleased, newReleased, keepReleased atomic.Int32

	pool := newBridgePool()
	defer pool.closeAll()
	busy := fakeBridge("busy", base.Add(-4*time.Minute), &busyReleased)
	if _, err := busy.attach(make(chan agent.StreamEvent, 1), "turn-1"); err != nil {
		t.Fatal(err)
	}
	busy.parkedAt = base.Add(-4 * time.Minute) // attach clears it; keep it oldest
	pool.bridges["busy"] = busy
	old := fakeBridge("old", base.Add(-3*time.Minute), &oldReleased)
	recent := fakeBridge("new", base.Add(-time.Minute), &newReleased)
	pool.bridges["old"] = old
	pool.bridges["new"] = recent

	// The bridge being parked is the oldest of all, and still exempt.
	keep := fakeBridge("keep", base.Add(-5*time.Minute), &keepReleased)
	if !pool.put(keep) {
		t.Fatal("open pool refused a bridge")
	}
	eventually(t, "the over-cap bridge to be released", func() bool { return oldReleased.Load() == 1 })
	if pool.parked("old") != nil || !old.isDead() {
		t.Fatal("the retired bridge is still parked or alive")
	}
	for name, b := range map[string]*residentBridge{"busy": busy, "new": recent, "keep": keep} {
		if pool.parked(name) != b || b.isDead() {
			t.Fatalf("bridge %q was evicted; only the least recently parked idle one should go", name)
		}
	}
	if busyReleased.Load()+newReleased.Load()+keepReleased.Load() != 0 {
		t.Fatal("a surviving bridge had its slot released")
	}
}

func TestPutReplacesAnEarlierBridgeForTheSameConversation(t *testing.T) {
	base := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	pinClock(t, base)
	var oldReleased, newReleased atomic.Int32
	pool := newBridgePool()
	defer pool.closeAll()
	old := fakeBridge("conv", base.Add(-time.Minute), &oldReleased)
	replacement := fakeBridge("conv", base, &newReleased)
	pool.put(old)
	pool.put(replacement)
	if pool.parked("conv") != replacement {
		t.Fatal("the newer bridge did not take the conversation's slot")
	}
	eventually(t, "the replaced bridge to be retired", func() bool { return oldReleased.Load() == 1 && old.isDead() })
	if newReleased.Load() != 0 || replacement.isDead() {
		t.Fatal("the replacement bridge was retired")
	}
}

func TestTakeHandsOutAMatchingBridgeAndForgetsIt(t *testing.T) {
	var released atomic.Int32
	pool := newBridgePool()
	defer pool.closeAll()
	b := fakeBridge("conv", time.Now(), &released)
	pool.put(b)
	got, notice := pool.take("conv", "fp")
	if got != b || notice != "" {
		t.Fatalf("take = %p, %q; want the parked bridge and no notice", got, notice)
	}
	if pool.parkedCount() != 0 {
		t.Fatal("a taken bridge is still parked")
	}
	if released.Load() != 0 || b.isDead() {
		t.Fatal("take retired the bridge it handed out")
	}
}

func TestTakeLeavesAnAttachedBridgeParked(t *testing.T) {
	var released atomic.Int32
	pool := newBridgePool()
	defer pool.closeAll()
	b := fakeBridge("conv", time.Now(), &released)
	pool.bridges["conv"] = b
	if _, err := b.attach(make(chan agent.StreamEvent, 1), "turn-1"); err != nil {
		t.Fatal(err)
	}
	if got, notice := pool.take("conv", "fp"); got != nil || notice != "" {
		t.Fatalf("take of an attached bridge = %p, %q; want nil and no notice", got, notice)
	}
	if pool.parked("conv") != b {
		t.Fatal("an attached bridge was dropped from the pool instead of put back")
	}
	if b.isDead() {
		t.Fatal("an attached bridge was retired by take")
	}
}

func TestTakeWithSettingsChangeRetiresSynchronously(t *testing.T) {
	var released atomic.Int32
	pool := newBridgePool()
	defer pool.closeAll()
	b := fakeBridge("conv", time.Now(), &released)
	pool.put(b)
	got, notice := pool.take("conv", "different")
	if got != nil || notice != "settings changed for this turn" {
		t.Fatalf("take with a new fingerprint = %p, %q", got, notice)
	}
	// markDead, not the async retire: the slot is free when take returns.
	if released.Load() != 1 || !b.isDead() {
		t.Fatalf("stale bridge released=%d dead=%v, want retired before take returns", released.Load(), b.isDead())
	}
	if pool.parkedCount() != 0 {
		t.Fatal("a stale bridge is still parked")
	}
}

func TestTakeOfNothingParked(t *testing.T) {
	var nilPool *bridgePool
	if got, notice := nilPool.take("conv", "fp"); got != nil || notice != "" {
		t.Fatal("a nil pool handed something out")
	}
	pool := newBridgePool()
	if got, notice := pool.take("", "fp"); got != nil || notice != "" {
		t.Fatal("an empty key matched a bridge")
	}
	if got, notice := pool.take("missing", "fp"); got != nil || notice != "" {
		t.Fatal("an unknown key matched a bridge")
	}
}

func TestCloseAllWaitsForEveryBridgeToBeReleased(t *testing.T) {
	var released atomic.Int32
	pool := newBridgePool()
	for i := 0; i < 3; i++ {
		pool.put(fakeBridge(fmt.Sprintf("conv-%d", i), time.Now(), &released))
	}
	pool.closeAll()
	if released.Load() != 3 {
		t.Fatalf("released after closeAll = %d, want 3 before it returns", released.Load())
	}
	if pool.parkedCount() != 0 {
		t.Fatal("closeAll left bridges parked")
	}
	if pool.put(fakeBridge("late", time.Now(), &released)) {
		t.Fatal("a shut-down pool accepted a bridge")
	}
}

// Concurrency shape of the production callers: turns taking and re-parking
// bridges for a few conversations, the drainer counting residents, and a
// shutdown landing mid-flight. Run under -race. Every bridge ever parked must
// end up either handed out or released — none may leak past closeAll.
func TestBridgePoolSurvivesConcurrentUseAndShutdown(t *testing.T) {
	setMaxParked(t, 2)
	pool := newBridgePool()
	var released atomic.Int32
	var mu sync.Mutex
	var created []*residentBridge
	var wg sync.WaitGroup
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				key := fmt.Sprintf("conv-%d", (g+i)%4)
				if b, _ := pool.take(key, "fp"); b != nil {
					if !pool.put(b) {
						_ = b.markDead(errPoolClosed)
					}
					continue
				}
				b := fakeBridge(key, time.Now(), &released)
				mu.Lock()
				created = append(created, b)
				mu.Unlock()
				if !pool.put(b) {
					_ = b.markDead(errPoolClosed)
				}
				_ = pool.activeResidentCount()
			}
		}(g)
	}
	time.Sleep(2 * time.Millisecond)
	pool.closeAll()
	wg.Wait()
	if pool.parkedCount() != 0 {
		t.Fatal("bridges parked after shutdown")
	}
	mu.Lock()
	total := int32(len(created))
	mu.Unlock()
	eventually(t, "every bridge to be released", func() bool { return released.Load() == total })
}
