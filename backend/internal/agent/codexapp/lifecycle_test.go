package codexapp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/codexcli"
	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
)

func TestThreadStartSendsSchemaSandboxCasing(t *testing.T) {
	spawner := &scriptedSpawner{}
	backend := NewBackend()
	ch := make(chan agent.StreamEvent, 32)
	if err := backend.RunWithSession(context.Background(), "hi", "/workspace", agent.RunRequest{
		ConfigDir: "/account", Spawner: spawner,
	}, ch); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	for range ch {
	}

	start := paramsOf(t, spawner, "thread/start")
	if got := start["sandbox"]; got != "danger-full-access" {
		t.Fatalf("thread/start sandbox = %v, want the kebab-case SandboxMode variant", got)
	}
	turn := paramsOf(t, spawner, "turn/start")
	policy, _ := turn["sandboxPolicy"].(map[string]any)
	if got := policy["type"]; got != "dangerFullAccess" {
		t.Fatalf("turn/start sandboxPolicy.type = %v, want the camelCase SandboxPolicy variant", got)
	}
	if _, ok := turn["effort"]; ok {
		t.Fatalf("turn/start unexpectedly forced an effort: %#v", turn)
	}
}

func TestThreadStartDeclaresContextWindow(t *testing.T) {
	spawner := &scriptedSpawner{}
	ch := make(chan agent.StreamEvent, 32)
	if err := NewBackend().RunWithSession(context.Background(), "hi", "/workspace", agent.RunRequest{
		ConfigDir: "/account", Spawner: spawner, ContextWindow: 131072,
	}, ch); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	for range ch {
	}
	cfg, _ := paramsOf(t, spawner, "thread/start")["config"].(map[string]any)
	if got := cfg["model_context_window"]; got != float64(131072) {
		t.Fatalf("thread/start config = %#v, want model_context_window 131072", cfg)
	}
}

func TestTurnStartSendsExplicitThinkLevel(t *testing.T) {
	spawner := &scriptedSpawner{}
	backend := NewBackend()
	ch := make(chan agent.StreamEvent, 32)
	if err := backend.RunWithSession(context.Background(), "hi", "/workspace", agent.RunRequest{
		ConfigDir: "/account", Spawner: spawner, ThinkLevel: "max",
	}, ch); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	for range ch {
	}
	if got := paramsOf(t, spawner, "turn/start")["effort"]; got != "xhigh" {
		t.Fatalf("turn/start effort = %v, want xhigh", got)
	}
}

func TestUnverifiedSessionIDStartsFreshThread(t *testing.T) {
	spawner := &scriptedSpawner{}
	backend := NewBackend()
	ch := make(chan agent.StreamEvent, 32)
	if err := backend.RunWithSession(context.Background(), "hi", "/workspace", agent.RunRequest{
		ConfigDir: "/account", Spawner: spawner, SessionID: "not-on-disk", IsResume: false,
	}, ch); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	for event := range ch {
		if event.Kind == agent.KindSessionWarning {
			t.Fatalf("fresh thread emitted a resume warning: %s", event.Content)
		}
	}

	start := paramsOf(t, spawner, "thread/start")
	spawner.mu.Lock()
	defer spawner.mu.Unlock()
	if containsMethod(spawner.requests, "thread/resume") {
		t.Fatalf("requests = %#v, must not resume an unverified session", spawner.requests)
	}
	if _, ok := start["threadId"]; ok {
		t.Fatalf("thread/start reused the unverified session id: %#v", start)
	}
}

func TestResumeFailureFallsBackToFreshThread(t *testing.T) {
	spawner := &scriptedSpawner{resumeFails: true}
	backend := NewBackend()
	ch := make(chan agent.StreamEvent, 32)
	err := backend.RunWithSession(context.Background(), "hi", "/workspace", agent.RunRequest{
		ConfigDir: "/account", Spawner: spawner, SessionID: "gone-thread", IsResume: true,
	}, ch)
	if err != nil {
		t.Fatalf("a resume of a vanished thread must not fail the turn: %v", err)
	}
	var sawInit, sawWarning bool
	for event := range ch {
		if event.Kind == agent.KindSessionWarning {
			sawWarning = true
			if !strings.Contains(event.Content, "gone-thread") || !strings.Contains(event.Content, "could not resume") {
				t.Fatalf("session warning = %q, want old thread and resume reason", event.Content)
			}
		}
		if event.Kind == agent.KindSystemInit {
			sawInit = true
			if strings.Contains(event.Content, "gone-thread") {
				t.Fatalf("system_init still advertises the dead thread: %s", event.Content)
			}
		}
	}
	if !sawInit {
		t.Fatal("no system_init emitted for the replacement thread")
	}
	if !sawWarning {
		t.Fatal("no session warning emitted for the replacement thread")
	}
	spawner.mu.Lock()
	defer spawner.mu.Unlock()
	if !containsMethod(spawner.requests, "thread/resume") || !containsMethod(spawner.requests, "thread/start") {
		t.Fatalf("requests = %#v, want a resume attempt followed by a fresh start", spawner.requests)
	}
}

func TestResumeTransportFailureReplacesServerBeforeFreshThread(t *testing.T) {
	spawner := &scriptedSpawner{resumeDropsTransport: true}
	backend := NewBackend()
	ch := make(chan agent.StreamEvent, 32)
	err := backend.RunWithSession(context.Background(), "hi", "/workspace", agent.RunRequest{
		ConfigDir: "/account", Spawner: spawner, SessionID: "large-thread", IsResume: true,
	}, ch)
	if err != nil {
		t.Fatalf("a broken resume transport must fall back on a replacement server: %v", err)
	}
	var warning string
	for event := range ch {
		if event.Kind == agent.KindSessionWarning {
			warning = event.Content
		}
	}
	if !strings.Contains(warning, "large-thread") || !strings.Contains(warning, "fresh thread") {
		t.Fatalf("session warning = %q, want transport fallback details", warning)
	}

	spawner.mu.Lock()
	defer spawner.mu.Unlock()
	if spawner.spawnCount != 2 {
		t.Fatalf("spawn count = %d, want a replacement server", spawner.spawnCount)
	}
	if !containsMethod(spawner.requests, "thread/resume") || !containsMethod(spawner.requests, "thread/start") {
		t.Fatalf("requests = %#v, want resume followed by fresh start", spawner.requests)
	}
}

func TestReconnectNarrationDoesNotAbortTheTurn(t *testing.T) {
	spawner := &scriptedSpawner{streamRetries: 3}
	backend := NewBackend()
	ch := make(chan agent.StreamEvent, 32)
	if err := backend.RunWithSession(context.Background(), "hi", "/workspace", agent.RunRequest{
		ConfigDir: "/account", Spawner: spawner,
	}, ch); err != nil {
		t.Fatalf("a turn codex is still retrying must not fail: %v", err)
	}
	var result string
	for event := range ch {
		if event.Kind == agent.KindResult {
			result = event.Content
		}
		if strings.Contains(event.Content, "Reconnecting") {
			t.Fatalf("retry narration leaked into the transcript as a %s event: %s", event.Kind, event.Content)
		}
	}
	if result != "hello" {
		t.Fatalf("result = %q, want the answer that arrived after the reconnects", result)
	}
}

func TestSilentTurnTripsTheStallWatchdog(t *testing.T) {
	spawner := &scriptedSpawner{silentTurn: true}
	backend := NewBackend()
	ch := make(chan agent.StreamEvent, 32)
	done := make(chan error, 1)
	go func() {
		done <- backend.RunWithSession(context.Background(), "hi", "/workspace", agent.RunRequest{
			ConfigDir: "/account", Spawner: spawner, StallTimeout: 50 * time.Millisecond,
		}, ch)
	}()
	for range ch {
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "upstream unreachable") {
			t.Fatalf("error = %v, want an upstream-unreachable stall", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a silent turn hung instead of stalling out")
	}
}

func TestOverflowedSubscriptionFailsOnlyItsOwnTurn(t *testing.T) {
	sub := newSubscription()
	for i := 0; i < subscriptionBuffer+8; i++ {
		sub.deliver(wireMessage{Method: "item/agentMessage/delta"})
	}
	select {
	case <-sub.lost:
	default:
		t.Fatal("an overflowed subscription must report the gap")
	}

	spawner := &scriptedSpawner{}
	r := NewBackend().(*runner)
	srv, release, err := r.pool.get(context.Background(), agent.RunRequest{ConfigDir: "/account", Spawner: spawner}, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	out := make(chan agent.StreamEvent, 8)
	go func() {
		for range out {
		}
	}()
	defer close(out)
	err = r.pumpTurn(context.Background(), srv, sub, "thread-1", "turn-1", "/workspace", agent.RunRequest{StallTimeout: time.Minute}, streamcommon.NewEmitter(context.Background(), out, "test"))
	if !errors.Is(err, errEventBacklogOverflow) {
		t.Fatalf("error = %v, want %v", err, errEventBacklogOverflow)
	}
}

// A consumer that stops reading used to wedge pumpTurn on a bare channel
// send — and pumpTurn is the loop that also services the stall watchdog, so
// the turn hung with no mechanism left to notice. The emitter bounds the send
// so the turn ends with the reason instead.
func TestAbandonedConsumerEndsTheTurn(t *testing.T) {
	sub := newSubscription()
	for i := 0; i < 4; i++ {
		sub.deliver(wireMessage{
			Method: "item/agentMessage/delta",
			Params: json.RawMessage(`{"threadId":"t","itemId":"i","delta":"hello"}`),
		})
	}

	spawner := &scriptedSpawner{}
	r := NewBackend().(*runner)
	srv, release, err := r.pool.get(context.Background(), agent.RunRequest{ConfigDir: "/account", Spawner: spawner}, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	// Unbuffered and never read: the shape of a consumer stuck in its own
	// persistence path.
	out := make(chan agent.StreamEvent)
	em := streamcommon.NewEmitter(context.Background(), out, "test").WithTimeout(20 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		done <- r.pumpTurn(context.Background(), srv, sub, "t", "u", "/workspace",
			agent.RunRequest{StallTimeout: time.Minute}, em)
	}()

	select {
	case err := <-done:
		if !errors.Is(err, streamcommon.ErrConsumerStalled) {
			t.Fatalf("error = %v, want ErrConsumerStalled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pumpTurn hung on an abandoned consumer")
	}
}

func TestIdleServersAreReclaimedButBusyOnesSurvive(t *testing.T) {
	clock := time.Now()
	nowFunc = func() time.Time { return clock }
	t.Cleanup(func() { nowFunc = time.Now })

	spawner := &scriptedSpawner{}
	pool := newServerPool()
	idle, releaseIdle, err := pool.get(context.Background(), agent.RunRequest{ConfigDir: "/idle", Spawner: spawner}, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	releaseIdle()
	busy, releaseBusy, err := pool.get(context.Background(), agent.RunRequest{ConfigDir: "/busy", Spawner: spawner}, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseBusy()

	clock = clock.Add(idleServerTTL + time.Minute)
	// A lookup for an unrelated account is what drives reclamation.
	if _, releaseThird, err := pool.get(context.Background(), agent.RunRequest{ConfigDir: "/third", Spawner: spawner}, "/workspace"); err != nil {
		t.Fatal(err)
	} else {
		releaseThird()
	}
	if idle.alive() {
		t.Fatal("an app-server idle past its TTL was not reclaimed")
	}
	if !busy.alive() {
		t.Fatal("an app-server with a turn in flight was reclaimed")
	}
}

func TestCloseAllRetiresEveryServer(t *testing.T) {
	spawner := &scriptedSpawner{}
	pool := newServerPool()
	srv, release, err := pool.get(context.Background(), agent.RunRequest{ConfigDir: "/account", Spawner: spawner}, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	release()
	pool.closeAll()
	if srv.alive() {
		t.Fatal("closeAll left a server running")
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if len(pool.servers) != 0 {
		t.Fatalf("pool still holds %d server(s)", len(pool.servers))
	}
}

func TestBackendRegistersItsPoolForShutdown(t *testing.T) {
	agent.CloseRegistered() // drain whatever earlier tests registered
	NewBackend()
	if n := agent.CloseRegistered(); n == 0 {
		t.Fatal("NewBackend did not register a shutdown closer for its pool")
	}
}

func TestPTYSpawnerIsDowngradedToPipes(t *testing.T) {
	pty := agent.RunRequest{ConfigDir: "/account", Spawner: &agent.PtySpawner{}}
	if _, isPty := effectiveSpawner(pty).(*agent.PtySpawner); isPty {
		t.Fatal("JSON-RPC over a pty would interleave with terminal echo")
	}
	if runtimeKey(pty) != runtimeKey(agent.RunRequest{ConfigDir: "/account"}) {
		t.Fatal("a downgraded pty request must share the pipe request's pool entry")
	}
}

func TestCapabilitiesMatchTheCliTransport(t *testing.T) {
	app := NewBackend().Capabilities()
	cli := codexcli.NewBackend().Capabilities()
	if app.SupportsCompaction != cli.SupportsCompaction {
		t.Fatalf("compaction support differs between transports: app=%v cli=%v", app.SupportsCompaction, cli.SupportsCompaction)
	}
	if !app.SupportsThinkingStream {
		t.Fatal("the app-server streams reasoning deltas")
	}
	// Both transports open codex threads, whose ids codex picks; a caller
	// minting one would hand the resume probe an id no rollout will ever have.
	if !app.AssignsSessionID || !cli.AssignsSessionID {
		t.Fatalf("codex assigns its own thread ids: app=%v cli=%v", app.AssignsSessionID, cli.AssignsSessionID)
	}
}

// The one capability the two Codex transports must NOT share. thread/tokenUsage/updated
// carries modelContextWindow; the CLI's token_count no longer does, so only this
// transport can back a context bar. Collapsing them again would either hide a
// working bar or resurrect the stale pinned-at-100% one on the CLI.
func TestOnlyTheAppServerTransportReportsContextUsage(t *testing.T) {
	if !NewBackend().Capabilities().ReportsContextUsage {
		t.Fatal("the app-server reports a real modelContextWindow and must advertise it")
	}
	if codexcli.NewBackend().Capabilities().ReportsContextUsage {
		t.Fatal("codex over the CLI emits no context_usage; advertising it would surface a stale replayed row")
	}
}

func TestSessionProbesReadTheRolloutTree(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "08", "12")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionID := "019ff674-bbb6-77a1-b0fa-0c4a8753eef2"
	path := filepath.Join(dir, "rollout-2026-08-12T14-52-12-"+sessionID+".jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	backend := NewBackend()
	if !backend.SessionExists("/workspace", sessionID, home) {
		t.Fatal("SessionExists missed an existing rollout")
	}
	if got := backend.SessionLogPath("/workspace", sessionID, home); got != path {
		t.Fatalf("SessionLogPath = %q, want %q — the cancel path rolls this file back", got, path)
	}
	if backend.SessionExists("/workspace", "not-a-session", home) {
		t.Fatal("SessionExists claimed an unknown session exists")
	}
}

// gatedSpawner blocks Spawn until gate closes, simulating a cold start that
// hangs on process startup or the initialize round-trip.
type gatedSpawner struct {
	inner   *scriptedSpawner
	entered chan struct{}
	gate    chan struct{}
	once    sync.Once
}

func (g *gatedSpawner) Spawn(ctx context.Context, req agent.SpawnRequest) (agent.RunningProcess, error) {
	g.once.Do(func() { close(g.entered) })
	<-g.gate
	return g.inner.Spawn(ctx, req)
}

func TestColdStartDoesNotBlockOtherAccounts(t *testing.T) {
	pool := newServerPool()
	fast := &scriptedSpawner{}
	_, releaseWarm, err := pool.get(context.Background(), agent.RunRequest{ConfigDir: "/warm", Spawner: fast}, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	releaseWarm()

	slow := &gatedSpawner{inner: &scriptedSpawner{}, entered: make(chan struct{}), gate: make(chan struct{})}
	coldDone := make(chan error, 1)
	go func() {
		_, releaseCold, err := pool.get(context.Background(), agent.RunRequest{ConfigDir: "/cold", Spawner: slow}, "/workspace")
		if err == nil {
			releaseCold()
		}
		coldDone <- err
	}()
	<-slow.entered

	warmDone := make(chan error, 1)
	go func() {
		_, release, err := pool.get(context.Background(), agent.RunRequest{ConfigDir: "/warm", Spawner: fast}, "/workspace")
		if err == nil {
			release()
		}
		warmDone <- err
	}()
	select {
	case err := <-warmDone:
		if err != nil {
			t.Fatalf("warm lookup: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a warm account's lookup queued behind another account's cold spawn")
	}
	close(slow.gate)
	if err := <-coldDone; err != nil {
		t.Fatalf("cold start: %v", err)
	}
}

func TestCancelDuringTurnStartInterruptsTheOrphan(t *testing.T) {
	spawner := &scriptedSpawner{turnStartGate: make(chan struct{})}
	backend := NewBackend()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan agent.StreamEvent, 32)
	done := make(chan error, 1)
	go func() {
		done <- backend.RunWithSession(ctx, "hi", "/workspace", agent.RunRequest{ConfigDir: "/account", Spawner: spawner}, ch)
	}()
	waitFor(t, "turn/start on the wire", func() bool {
		spawner.mu.Lock()
		defer spawner.mu.Unlock()
		return containsMethod(spawner.requests, "turn/start")
	})
	cancel()
	// Give startTurn a beat to take the ctx branch, then let the scripted
	// server answer. Whichever side wins the race, the started turn must end
	// up interrupted rather than running unattended.
	time.Sleep(20 * time.Millisecond)
	close(spawner.turnStartGate)
	for range ch {
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	waitFor(t, "turn/interrupt for the orphan", func() bool {
		spawner.mu.Lock()
		defer spawner.mu.Unlock()
		return containsMethod(spawner.requests, "turn/interrupt")
	})
}

func TestSecondResultChunkDoesNotDropTheFirst(t *testing.T) {
	spawner := &scriptedSpawner{}
	r := NewBackend().(*runner)
	srv, release, err := r.pool.get(context.Background(), agent.RunRequest{ConfigDir: "/account", Spawner: spawner}, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	sub := newSubscription()
	message := func(text string) wireMessage {
		return wireMessage{Method: "item/completed", Params: json.RawMessage(`{"threadId":"t","item":{"id":"m","type":"agentMessage","text":"` + text + `"}}`)}
	}
	// Two agent messages complete back to back with no usage frame between
	// them — the shape that used to overwrite the held first chunk.
	sub.deliver(message("first"))
	sub.deliver(message("second"))
	sub.deliver(wireMessage{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"t","turn":{"id":"u","status":"completed"}}`)})

	out := make(chan agent.StreamEvent, 16)
	if err := r.pumpTurn(context.Background(), srv, sub, "t", "u", "/workspace", agent.RunRequest{StallTimeout: time.Minute}, streamcommon.NewEmitter(context.Background(), out, "test")); err != nil {
		t.Fatalf("pumpTurn: %v", err)
	}
	close(out)
	var results []string
	for evt := range out {
		if evt.Kind == agent.KindResult {
			results = append(results, evt.Content)
		}
	}
	if len(results) != 2 || results[0] != "first" || results[1] != "second" {
		t.Fatalf("results = %v, want both chunks in order", results)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func paramsOf(t *testing.T, s *scriptedSpawner, method string) map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, request := range s.requests {
		if request["method"] == method {
			params, _ := request["params"].(map[string]any)
			return params
		}
	}
	t.Fatalf("no %s request was sent", method)
	return nil
}

// The TTL exists to keep a warm process for the pause between two messages,
// so shortening it must not start reclaiming servers inside that pause.
func TestServerIdleWithinTTLIsReused(t *testing.T) {
	clock := time.Now()
	nowFunc = func() time.Time { return clock }
	t.Cleanup(func() { nowFunc = time.Now })

	spawner := &scriptedSpawner{}
	pool := newServerPool()
	opts := agent.RunRequest{ConfigDir: "/account", Spawner: spawner}
	first, release, err := pool.get(context.Background(), opts, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	release()

	clock = clock.Add(idleServerTTL - time.Second)
	// An unrelated lookup drives reclamation; the pause has not run out yet.
	if _, releaseOther, err := pool.get(context.Background(), agent.RunRequest{ConfigDir: "/other", Spawner: spawner}, "/workspace"); err != nil {
		t.Fatal(err)
	} else {
		releaseOther()
	}

	second, releaseSecond, err := pool.get(context.Background(), opts, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSecond()
	if second != first {
		t.Error("a server idle for less than its TTL was replaced instead of reused")
	}
}
