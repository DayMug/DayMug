package imbot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeConnector implements Connector for supervision tests. Start outcomes
// are scripted through the shared fakePlatform that created it.
type fakeConnector struct {
	platform  string
	startErr  error
	responder Responder
	// dieOnStart makes an established connection report itself down after
	// dieAfter, modelling a long connection that never really works even
	// though the credential check in Start passes.
	dieOnStart error
	dieAfter   time.Duration

	mu        sync.Mutex
	onMessage func(Message)
	onDown    func(error)
	stops     int
}

func (f *fakeConnector) Platform() string            { return f.platform }
func (f *fakeConnector) Capabilities() Capabilities  { return platforms[f.platform].caps }
func (f *fakeConnector) Responder(Message) Responder { return f.responder }
func (f *fakeConnector) MentionTag() string          { return "<@" + f.platform + ">" }

func (f *fakeConnector) Start(context.Context) error {
	if f.startErr != nil {
		return f.startErr
	}
	if f.dieOnStart == nil {
		return nil
	}
	if f.dieAfter > 0 {
		go func() {
			time.Sleep(f.dieAfter)
			f.die(f.dieOnStart)
		}()
		return nil
	}
	f.die(f.dieOnStart)
	return nil
}

func (f *fakeConnector) SetOnMessage(fn func(Message)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onMessage = fn
}

func (f *fakeConnector) SetOnDown(fn func(err error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onDown = fn
}

func (f *fakeConnector) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	f.onMessage = nil
}

func (f *fakeConnector) stopCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stops
}

// emit pushes an inbound message through whatever sink is currently installed,
// the way a live platform goroutine would.
func (f *fakeConnector) emit(msg Message) {
	f.mu.Lock()
	sink := f.onMessage
	f.mu.Unlock()
	if sink != nil {
		sink(msg)
	}
}

func (f *fakeConnector) die(err error) {
	f.mu.Lock()
	report := f.onDown
	f.mu.Unlock()
	if report != nil {
		report(err)
	}
}

// fakePlatform registers a scripted connector factory under a synthetic
// platform name and records every connector it built, keyed by bot id.
type fakePlatform struct {
	name string

	mu        sync.Mutex
	failures  map[string]int   // bot id → remaining Start failures
	startErrs map[string]error // bot id → persistent Start failure
	flapping  map[string]bool  // bot id → every connection dies once established
	built     map[string][]*fakeConnector
	builtAt   map[string][]time.Time
}

func newFakePlatform(t *testing.T, name string) *fakePlatform {
	t.Helper()
	p := &fakePlatform{
		name: name, failures: map[string]int{}, startErrs: map[string]error{}, flapping: map[string]bool{},
		built: map[string][]*fakeConnector{}, builtAt: map[string][]time.Time{},
	}
	platforms[name] = platformDescriptor{
		caps: Capabilities{DisplayName: name, PromptName: name, SystemPromptName: name},
		factory: func(bot BotConfig) Connector {
			p.mu.Lock()
			defer p.mu.Unlock()
			c := &fakeConnector{platform: name, startErr: p.startErrs[bot.ID]}
			if p.failures[bot.ID] > 0 {
				p.failures[bot.ID]--
				c.startErr = errors.New("scripted start failure")
			}
			if p.flapping[bot.ID] {
				c.dieOnStart = errors.New("socket closed right after the handshake")
			}
			p.built[bot.ID] = append(p.built[bot.ID], c)
			p.builtAt[bot.ID] = append(p.builtAt[bot.ID], time.Now())
			return c
		},
	}
	t.Cleanup(func() { delete(platforms, name) })
	return p
}

func (p *fakePlatform) buildCount(botID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.built[botID])
}

func (p *fakePlatform) buildTimes(botID string) []time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Time(nil), p.builtAt[botID]...)
}

func (p *fakePlatform) lastBuilt(botID string) *fakeConnector {
	p.mu.Lock()
	defer p.mu.Unlock()
	if built := p.built[botID]; len(built) > 0 {
		return built[len(built)-1]
	}
	return nil
}

func newTestManager(t *testing.T, h Handler) *Manager {
	t.Helper()
	m := NewManager(h)
	m.RetryBaseDelay = time.Millisecond
	m.RetryMaxDelay = 2 * time.Millisecond
	t.Cleanup(m.Stop)
	return m
}

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}

func botCfg(agentID, botID, platform, token string) AgentBotConfig {
	return AgentBotConfig{AgentID: agentID, Bot: BotConfig{
		ID: botID, Platform: platform, Enabled: true, BotToken: token, AppToken: "app",
	}}
}

func runningStatus(m *Manager, botID string) func() bool {
	return func() bool {
		for _, st := range m.Statuses("") {
			if st.BotID == botID && st.Running && st.Error == "" {
				return true
			}
		}
		return false
	}
}

type markerResponder struct{ Responder }

func TestManagerReturnsResponderOnlyForHealthyConnector(t *testing.T) {
	p := newFakePlatform(t, "fake-responder")
	m := newTestManager(t, nil)
	m.Apply(Config{Bots: []AgentBotConfig{botCfg("agent1", "botA", p.name, "t1")}})
	eventually(t, runningStatus(m, "botA"), "botA never became running")

	want := &markerResponder{}
	connector := p.lastBuilt("botA")
	connector.responder = want
	got, ok := m.ResponderFor("botA", Message{ChannelID: "C1", ThreadID: "t1"})
	if !ok || got != want {
		t.Fatalf("healthy responder = (%T, %v), want (%T, true)", got, ok, want)
	}

	p.mu.Lock()
	p.failures["botA"] = 100
	p.mu.Unlock()
	connector.die(errors.New("connection lost"))
	eventually(t, func() bool {
		_, ok := m.ResponderFor("botA", Message{})
		return !ok
	}, "down connector remained available for outbound replies")
}

func TestManagerAppliesIncrementally(t *testing.T) {
	p := newFakePlatform(t, "fake-incr")
	m := newTestManager(t, nil)

	cfg := Config{Bots: []AgentBotConfig{
		botCfg("agent1", "botA", p.name, "t1"),
		botCfg("agent1", "botB", p.name, "t1"),
	}}
	m.Apply(cfg)
	eventually(t, runningStatus(m, "botA"), "botA never became running")
	eventually(t, runningStatus(m, "botB"), "botB never became running")

	// Re-applying an identical config must not bounce healthy connections.
	m.Apply(cfg)
	time.Sleep(20 * time.Millisecond)
	if got := p.buildCount("botA"); got != 1 {
		t.Fatalf("unchanged botA was rebuilt %d times, want 1", got)
	}

	// A credential change rebuilds only the affected bot.
	m.Apply(Config{Bots: []AgentBotConfig{
		botCfg("agent1", "botA", p.name, "t2"),
		botCfg("agent1", "botB", p.name, "t1"),
	}})
	eventually(t, func() bool { return p.buildCount("botA") == 2 }, "changed botA was not rebuilt")
	time.Sleep(20 * time.Millisecond)
	if got := p.buildCount("botB"); got != 1 {
		t.Fatalf("unchanged botB was rebuilt %d times, want 1", got)
	}

	// Removing a bot drops it from the status list.
	m.Apply(Config{Bots: []AgentBotConfig{botCfg("agent1", "botA", p.name, "t2")}})
	eventually(t, func() bool { return len(m.Statuses("")) == 1 }, "removed botB still reported")
}

func TestManagerRetriesFailedStart(t *testing.T) {
	p := newFakePlatform(t, "fake-retry")
	p.failures["botA"] = 2
	m := newTestManager(t, nil)

	m.Apply(Config{Bots: []AgentBotConfig{botCfg("agent1", "botA", p.name, "t1")}})
	eventually(t, runningStatus(m, "botA"), "bot never recovered from failed starts")
	if got := p.buildCount("botA"); got != 3 {
		t.Fatalf("start attempts = %d, want 3 (2 failures + 1 success)", got)
	}
}

type testPermanentConnectorError struct{ message string }

func (e testPermanentConnectorError) Error() string   { return e.message }
func (e testPermanentConnectorError) Permanent() bool { return true }

func TestPermanentConnectorErrorClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "typed platform error", err: testPermanentConnectorError{"revoked token"}, want: true},
		{name: "wrapped typed error", err: fmt.Errorf("start: %w", testPermanentConnectorError{"revoked token"}), want: true},
		{name: "slack auth rejection", err: errors.New("slack auth.test: invalid_auth"), want: true},
		{name: "http forbidden", err: errors.New("wechat getupdates: HTTP 403"), want: true},
		{name: "permission denial", err: errors.New("feishu: permission denied"), want: true},
		{name: "invalid app secret", err: errors.New("feishu: appSecret is invalid"), want: true},
		{name: "bot capability disabled", err: errors.New("请确认已开启机器人能力"), want: true},
		{name: "temporary network error", err: errors.New("connection reset by peer"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPermanentConnectorError(tt.err); got != tt.want {
				t.Fatalf("isPermanentConnectorError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestManagerDoesNotRetryPermanentFailure(t *testing.T) {
	p := newFakePlatform(t, "fake-permanent")
	p.startErrs["botA"] = testPermanentConnectorError{"credentials rejected"}
	m := newTestManager(t, nil)

	m.Apply(Config{Bots: []AgentBotConfig{botCfg("agent1", "botA", p.name, "t1")}})
	eventually(t, func() bool {
		statuses := m.Statuses("")
		return len(statuses) == 1 && statuses[0].Error == "credentials rejected"
	}, "permanent failure was not surfaced")
	time.Sleep(20 * time.Millisecond)
	if got := p.buildCount("botA"); got != 1 {
		t.Fatalf("permanent failure started connector %d times, want exactly 1", got)
	}
}

func TestManagerReconnectsWhenConnectionDies(t *testing.T) {
	p := newFakePlatform(t, "fake-down")
	m := newTestManager(t, nil)

	m.Apply(Config{Bots: []AgentBotConfig{botCfg("agent1", "botA", p.name, "t1")}})
	eventually(t, runningStatus(m, "botA"), "bot never became running")

	p.lastBuilt("botA").die(errors.New("socket died"))
	eventually(t, func() bool { return p.buildCount("botA") >= 2 }, "dead connection was not rebuilt")
	eventually(t, runningStatus(m, "botA"), "bot never recovered after connection death")
}

// TestManagerStopsTheConnectorItAbandons covers the leak that made 飞书 bots go
// permanently silent: every Apply that changes a bot's credentials used to
// cancel a context both SDKs ignore, leaving the old long connection alive.
func TestManagerStopsTheConnectorItAbandons(t *testing.T) {
	p := newFakePlatform(t, "fake-teardown")
	h := &recordingHandler{}
	m := newTestManager(t, h)

	m.Apply(Config{Bots: []AgentBotConfig{botCfg("agent1", "botA", p.name, "t1")}})
	eventually(t, runningStatus(m, "botA"), "botA never became running")
	replaced := p.lastBuilt("botA")

	m.Apply(Config{Bots: []AgentBotConfig{botCfg("agent1", "botA", p.name, "t2")}})
	eventually(t, func() bool { return p.buildCount("botA") == 2 }, "changed botA was not rebuilt")
	eventually(t, func() bool { return replaced.stopCount() == 1 }, "the replaced connector was never stopped")

	// An event still in flight inside the abandoned connector must be dropped,
	// not dispatched under its dead context — the bridge would only log
	// "context canceled" and the user would never get an answer.
	replaced.emit(Message{Platform: p.name, ChannelID: "C1", MessageID: "stale"})
	time.Sleep(20 * time.Millisecond)
	if got := h.all(); len(got) != 0 {
		t.Fatalf("abandoned connector still dispatched %d message(s): %+v", len(got), got)
	}

	// The live generation keeps working.
	p.lastBuilt("botA").emit(Message{Platform: p.name, ChannelID: "C1", MessageID: "fresh"})
	eventually(t, func() bool { return len(h.all()) == 1 }, "live connector stopped delivering after the reload")
}

// A reconnect within one generation reuses the bot's context, so the previous
// attempt is only released if the supervisor stops it explicitly.
func TestManagerStopsEachFinishedAttempt(t *testing.T) {
	p := newFakePlatform(t, "fake-attempt")
	m := newTestManager(t, nil)

	m.Apply(Config{Bots: []AgentBotConfig{botCfg("agent1", "botA", p.name, "t1")}})
	eventually(t, runningStatus(m, "botA"), "bot never became running")
	first := p.lastBuilt("botA")

	first.die(errors.New("socket died"))
	eventually(t, func() bool { return p.buildCount("botA") >= 2 }, "dead connection was not rebuilt")
	eventually(t, func() bool { return first.stopCount() == 1 }, "the dead attempt was never stopped")
}

// TestManagerBackoffGrowsWhileConnectionFlaps pins the second half of the
// backoff fix: Start succeeding proves only that the REST credentials work, so
// a connection that dies immediately must still push the delay up.
func TestManagerBackoffGrowsWhileConnectionFlaps(t *testing.T) {
	p := newFakePlatform(t, "fake-unstable")
	p.flapping["botA"] = true
	m := NewManager(nil)
	m.RetryBaseDelay = 20 * time.Millisecond
	m.RetryMaxDelay = time.Second
	m.StableAfter = time.Hour // nothing in this test lives long enough to count
	t.Cleanup(m.Stop)

	m.Apply(Config{Bots: []AgentBotConfig{botCfg("agent1", "botA", p.name, "t1")}})
	eventually(t, func() bool { return p.buildCount("botA") >= 4 }, "flapping bot did not retry")

	times := p.buildTimes("botA")
	first := times[1].Sub(times[0])
	third := times[3].Sub(times[2])
	// 20ms → 40ms → 80ms with ±20% jitter; a reset-on-Start backoff keeps every
	// gap at the base delay instead.
	if third < 2*first {
		t.Fatalf("reconnect delay did not grow: gaps %s then %s", first, third)
	}
}

func TestRunConnectionReportsStabilityFromUptime(t *testing.T) {
	tests := []struct {
		name        string
		stableAfter time.Duration
		uptime      time.Duration
		want        bool
	}{
		{
			name:        "a connection that dies on arrival is not a working configuration",
			stableAfter: time.Hour,
			want:        false,
		},
		{
			name:        "a connection that outlives the threshold resets the backoff",
			stableAfter: 5 * time.Millisecond,
			uptime:      40 * time.Millisecond,
			want:        true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManager(nil)
			m.StableAfter = tt.stableAfter
			c := &fakeConnector{
				platform:   PlatformSlack,
				dieOnStart: errors.New("socket died"),
				dieAfter:   tt.uptime,
			}
			mb := &managedBot{key: connectorKey{botID: "botA", platform: PlatformSlack}}

			stable, err := m.runConnection(context.Background(), mb, c)
			if err == nil {
				t.Fatal("a dead connection must surface its error")
			}
			if stable != tt.want {
				t.Fatalf("stable = %v, want %v", stable, tt.want)
			}
			if c.stopCount() != 1 {
				t.Fatalf("connector stopped %d times, want exactly 1", c.stopCount())
			}
		})
	}
}

// Without jitter one Apply puts every bot on the same retry tick, so a shared
// outage or rate limiter keeps failing all of them in lockstep.
func TestJitteredSpreadsRetriesWithinBounds(t *testing.T) {
	const delay = 5 * time.Second
	seen := map[time.Duration]struct{}{}
	for range 200 {
		got := jittered(delay)
		if got < delay*4/5 || got > delay*6/5 {
			t.Fatalf("jittered(%s) = %s, outside ±20%%", delay, got)
		}
		seen[got] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatal("jitter produced a single value; bots would still retry in lockstep")
	}
	if got := jittered(0); got != 0 {
		t.Fatalf("jittered(0) = %s, want 0", got)
	}
}

type recordingHandler struct {
	mu   sync.Mutex
	msgs []Message
}

func (h *recordingHandler) HandleMessage(_ context.Context, msg Message, _ Responder) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, msg)
}

func (h *recordingHandler) all() []Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Message(nil), h.msgs...)
}

func TestManagerInjectsAgentAndBotIDs(t *testing.T) {
	p := newFakePlatform(t, "fake-inject")
	h := &recordingHandler{}
	m := newTestManager(t, h)

	m.Apply(Config{Bots: []AgentBotConfig{botCfg("agent1", "botA", p.name, "t1")}})
	eventually(t, runningStatus(m, "botA"), "bot never became running")

	p.lastBuilt("botA").emit(Message{Platform: p.name, ChannelID: "C1", MessageID: "m1"})
	eventually(t, func() bool { return len(h.all()) == 1 }, "message never reached the handler")
	got := h.all()[0]
	if got.AgentID != "agent1" || got.BotID != "botA" {
		t.Fatalf("agent/bot ids not injected: %+v", got)
	}

	// Redelivery of the same platform message id is dropped.
	p.lastBuilt("botA").emit(Message{Platform: p.name, ChannelID: "C1", MessageID: "m1"})
	time.Sleep(20 * time.Millisecond)
	if n := len(h.all()); n != 1 {
		t.Fatalf("redelivered message reached the handler: %d messages", n)
	}
}
