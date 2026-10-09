package claudeagentsdk

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// residencyProbe records what the adapter asked of its owner.
type residencyProbe struct {
	mu       sync.Mutex
	permits  int
	parked   int
	releases int
	notices  []string
	activity []bool
	allow    bool
	wakes    []chan agent.StreamEvent
	wakeDone chan error
}

func newResidencyProbe(allow bool) *residencyProbe {
	return &residencyProbe{allow: allow, wakeDone: make(chan error, 4)}
}

func (p *residencyProbe) residency() *agent.Residency {
	return &agent.Residency{
		Permit: func() (func(), bool) {
			p.mu.Lock()
			p.permits++
			allow := p.allow
			p.mu.Unlock()
			if !allow {
				return nil, false
			}
			return func() { p.mu.Lock(); p.releases++; p.mu.Unlock() }, true
		},
		Parked: func() {
			p.mu.Lock()
			p.parked++
			p.mu.Unlock()
		},
		Activity: func(active bool) {
			p.mu.Lock()
			p.activity = append(p.activity, active)
			p.mu.Unlock()
		},
		Wake: func() (chan<- agent.StreamEvent, func(error)) {
			ch := make(chan agent.StreamEvent, 32)
			p.mu.Lock()
			p.wakes = append(p.wakes, ch)
			p.mu.Unlock()
			return ch, func(err error) { p.wakeDone <- err }
		},
		Notice: func(reason string) {
			p.mu.Lock()
			p.notices = append(p.notices, reason)
			p.mu.Unlock()
		},
	}
}

func (p *residencyProbe) counts() (permits, releases, notices int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.permits, p.releases, len(p.notices)
}

func (p *residencyProbe) parkedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.parked
}

func (p *residencyProbe) noticeText() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.notices, " | ")
}

func (p *residencyProbe) wakeChannel(t *testing.T, index int) chan agent.StreamEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		if len(p.wakes) > index {
			ch := p.wakes[index]
			p.mu.Unlock()
			return ch
		}
		p.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("provider never asked for a wakeup sink #%d", index)
	return nil
}

func (p *residencyProbe) wakeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.wakes)
}

func (p *residencyProbe) activityStates() []bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]bool(nil), p.activity...)
}

// residentSDK is a fake whose query stays open across turns, replying once per
// input message. tasksPerTurn controls whether each turn looks like it left
// background work running.
func residentSDK(t *testing.T, tasksPerTurn int) string {
	t.Helper()
	tasks := "[]"
	if tasksPerTurn > 0 {
		tasks = `[{"task_id":"t1","task_type":"monitor","description":"probe"}]`
	}
	return residentSDKWithTasks(t, tasks)
}

func residentSDKWithTasks(t *testing.T, tasks string) string {
	t.Helper()
	return fakeSDKInstallWithSource(t, "9.9.9", `
export function query({ prompt }) {
  const gen = (async function* () {
    let n = 0;
    for await (const message of prompt) {
      n += 1;
      yield { type: "system", subtype: "background_tasks_changed", tasks: `+tasks+` };
      yield { type: "result", subtype: "success", is_error: false, result: "reply-" + n + ":" + message.message.content, user_message_uuid: message.uuid, origin: message.origin };
    }
  })();
  gen.interrupt = async () => ({});
  return gen;
}
`)
}

func runTurn(t *testing.T, backend agent.Backend, convID, prompt, workDir string, res *agent.Residency) ([]agent.StreamEvent, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	outputCh := make(chan agent.StreamEvent, 64)
	errCh := make(chan error, 1)
	go func() {
		errCh <- backend.RunWithSession(ctx, prompt, workDir, agent.RunRequest{
			ControlID: convID,
			Residency: res,
		}, outputCh)
	}()
	var events []agent.StreamEvent
	for evt := range outputCh {
		events = append(events, evt)
	}
	return events, <-errCh
}

func resultText(events []agent.StreamEvent) string {
	for _, evt := range events {
		if evt.Kind == agent.KindResult {
			return evt.Content
		}
	}
	return ""
}

func resultTexts(events []agent.StreamEvent) []string {
	var results []string
	for _, evt := range events {
		if evt.Kind == agent.KindResult {
			results = append(results, evt.Content)
		}
	}
	return results
}

// The whole point: a turn that leaves background work running keeps its
// process, so the wakeup that work will trigger has somewhere to arrive.
func TestTurnParksWhenBackgroundTasksAreLive(t *testing.T) {
	root := residentSDK(t, 1)
	withSearchDirs(t, []string{root}, root)
	backend := NewBackend()
	r := backend.(*runner)
	defer r.pool.closeAll()

	probe := newResidencyProbe(true)
	events, err := runTurn(t, backend, "conv-park", "go", t.TempDir(), probe.residency())
	if err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	if got := resultText(events); got != "reply-1:go" {
		t.Fatalf("result = %q", got)
	}
	if n := r.pool.parkedCount(); n != 1 {
		t.Fatalf("parked bridges = %d, want 1", n)
	}
	permits, releases, notices := probe.counts()
	if permits != 1 || releases != 0 || notices != 0 {
		t.Fatalf("permits=%d releases=%d notices=%d, want 1/0/0", permits, releases, notices)
	}
	if got := probe.activityStates(); len(got) != 1 || !got[0] {
		t.Fatalf("resident activity = %v, want [true] while background work is live", got)
	}
	r.pool.closeAll()
	if _, releases, _ := probe.counts(); releases != 1 {
		t.Fatalf("the live-process slot was not released on teardown (releases=%d)", releases)
	}
	if got := probe.activityStates(); len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("resident activity after teardown = %v, want [true false]", got)
	}
}

// The common case must cost nothing. With no background work the process is
// torn down at the turn boundary, exactly as it was before residency existed.
func TestTurnClosesProcessWhenNoBackgroundTasks(t *testing.T) {
	root := residentSDK(t, 0)
	withSearchDirs(t, []string{root}, root)
	backend := NewBackend()
	r := backend.(*runner)
	defer r.pool.closeAll()

	probe := newResidencyProbe(true)
	if _, err := runTurn(t, backend, "conv-plain", "go", t.TempDir(), probe.residency()); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	if n := r.pool.parkedCount(); n != 0 {
		t.Fatalf("parked bridges = %d, want 0", n)
	}
	if permits, _, _ := probe.counts(); permits != 0 {
		t.Fatalf("permit was consulted %d times with no background work, want 0", permits)
	}
	if got := probe.activityStates(); len(got) != 0 {
		t.Fatalf("plain turn reported resident activity: %v", got)
	}
}

// The SDK keeps internal watchers in the same process-level task snapshot as
// user work. They are explicitly marked ambient and must not buy residency.
func TestTurnClosesProcessWhenOnlyAmbientTasksRemain(t *testing.T) {
	root := residentSDKWithTasks(t, `[{"task_id":"watcher","task_type":"watcher","ambient":true}]`)
	withSearchDirs(t, []string{root}, root)
	backend := NewBackend()
	r := backend.(*runner)
	defer r.pool.closeAll()

	probe := newResidencyProbe(true)
	if _, err := runTurn(t, backend, "conv-ambient", "go", t.TempDir(), probe.residency()); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	if n := r.pool.parkedCount(); n != 0 {
		t.Fatalf("parked bridges = %d, want 0", n)
	}
	if permits, _, _ := probe.counts(); permits != 0 {
		t.Fatalf("permit was consulted %d times for ambient-only work, want 0", permits)
	}
	if got := probe.activityStates(); len(got) != 0 {
		t.Fatalf("ambient-only turn reported resident activity: %v", got)
	}
}

// Refusing residency is not an error, but it does mean this turn's background
// work dies — which the user has to be told, or a long job just vanishes.
func TestPermitRefusedTearsDownAndSaysSo(t *testing.T) {
	root := residentSDK(t, 1)
	withSearchDirs(t, []string{root}, root)
	backend := NewBackend()
	r := backend.(*runner)
	defer r.pool.closeAll()

	probe := newResidencyProbe(false)
	if _, err := runTurn(t, backend, "conv-refused", "go", t.TempDir(), probe.residency()); err != nil {
		t.Fatalf("a refused permit must not fail the turn: %v", err)
	}
	if n := r.pool.parkedCount(); n != 0 {
		t.Fatalf("parked bridges = %d, want 0", n)
	}
	permits, releases, notices := probe.counts()
	if permits != 1 || releases != 0 || notices != 1 {
		t.Fatalf("permits=%d releases=%d notices=%d, want 1/0/1", permits, releases, notices)
	}
	if !strings.Contains(probe.noticeText(), "background work") {
		t.Fatalf("notice does not mention the lost work: %q", probe.noticeText())
	}
}

// Reuse is the half of residency that is easy to miss: respawning with
// --resume would signal the process group and kill the very background work
// the bridge was parked for.
func TestNextTurnReusesTheParkedProcess(t *testing.T) {
	root := residentSDK(t, 1)
	withSearchDirs(t, []string{root}, root)
	backend := NewBackend()
	r := backend.(*runner)
	defer r.pool.closeAll()

	probe := newResidencyProbe(true)
	workDir := t.TempDir()
	if _, err := runTurn(t, backend, "conv-reuse", "first", workDir, probe.residency()); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	events, err := runTurn(t, backend, "conv-reuse", "second", workDir, probe.residency())
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	// "reply-2" can only come from the same process: a fresh spawn would
	// have restarted the fake's per-process counter at 1.
	if got := resultText(events); got != "reply-2:second" {
		t.Fatalf("result = %q, want the parked process to have handled it", got)
	}
	if n := r.pool.parkedCount(); n != 1 {
		t.Fatalf("parked bridges = %d, want 1", n)
	}
	permits, releases, _ := probe.counts()
	if permits != 1 || releases != 0 {
		t.Fatalf("reused process permits=%d releases=%d, want 1/0", permits, releases)
	}
	if parked := probe.parkedCount(); parked != 2 {
		t.Fatalf("parked callback count = %d, want one per turn", parked)
	}
	r.pool.closeAll()
	if _, releases, _ := probe.counts(); releases != 1 {
		t.Fatalf("reused process released its live slot %d times, want exactly 1", releases)
	}
}

func TestReusedBridgeTransfersResidentActivitySubscription(t *testing.T) {
	root := residentSDK(t, 1)
	withSearchDirs(t, []string{root}, root)
	backend := NewBackend()
	r := backend.(*runner)
	defer r.pool.closeAll()

	first := newResidencyProbe(true)
	second := newResidencyProbe(true)
	workDir := t.TempDir()
	if _, err := runTurn(t, backend, "conv-activity-transfer", "first", workDir, first.residency()); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if _, err := runTurn(t, backend, "conv-activity-transfer", "second", workDir, second.residency()); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got := first.activityStates(); len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("old activity subscription = %v, want [true false]", got)
	}
	if got := second.activityStates(); len(got) != 1 || !got[0] {
		t.Fatalf("new activity subscription = %v, want [true]", got)
	}
	r.pool.closeAll()
	if got := second.activityStates(); len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("new activity subscription after teardown = %v, want [true false]", got)
	}
}

// A turn the provider started on its own — the failure this whole feature
// exists to fix.
func TestWakeupTurnIsDeliveredWithoutAPrompt(t *testing.T) {
	root := fakeSDKInstallWithSource(t, "9.9.9", `
export function query({ prompt }) {
  const gen = (async function* () {
    const it = prompt[Symbol.asyncIterator]();
    const first = await it.next();
    yield { type: "system", subtype: "background_tasks_changed", tasks: [{ task_id: "t1" }] };
    yield { type: "result", subtype: "success", is_error: false, result: "armed", user_message_uuid: first.value.uuid, origin: first.value.origin };
    await new Promise((resolve) => setTimeout(resolve, 100));
    yield { type: "system", subtype: "init", model: "m", session_id: "s", tools: [] };
    yield { type: "result", subtype: "success", is_error: false, result: "the codex leg finished", origin: { kind: "task-notification" } };
    yield { type: "system", subtype: "background_tasks_changed", tasks: [] };
    await new Promise(() => {});
  })();
  gen.interrupt = async () => ({});
  return gen;
}
`)
	withSearchDirs(t, []string{root}, root)
	backend := NewBackend()
	r := backend.(*runner)
	defer r.pool.closeAll()

	probe := newResidencyProbe(true)
	if _, err := runTurn(t, backend, "conv-wake", "arm it", t.TempDir(), probe.residency()); err != nil {
		t.Fatalf("first turn: %v", err)
	}

	ch := probe.wakeChannel(t, 0)
	deadline := time.After(10 * time.Second)
	var woke string
	for woke == "" {
		select {
		case evt := <-ch:
			if evt.Kind == agent.KindResult {
				woke = evt.Content
			}
		case <-deadline:
			t.Fatal("the provider-initiated turn never arrived")
		}
	}
	if woke != "the codex leg finished" {
		t.Fatalf("wakeup result = %q", woke)
	}
	select {
	case err := <-probe.wakeDone:
		if err != nil {
			t.Fatalf("wakeup turn settled with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wakeup turn was never settled with its owner")
	}
	// The SDK commonly emits the empty task level after the autonomous
	// result. It is lifecycle bookkeeping, not a second provider turn.
	time.Sleep(200 * time.Millisecond)
	if got := probe.wakeCount(); got != 1 {
		t.Fatalf("trailing empty task level created %d wakeups, want exactly 1", got)
	}
	if got := probe.activityStates(); len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("wakeup resident activity = %v, want [true false]", got)
	}
}

// A background sub-agent keeps streaming while its parent is parked. That is
// the sub-agent working, not the parent reacting: waking on it opened a turn
// no result would end until the sub-agent finished, so the chat showed
// "thinking" instead of the background Stop button the whole time.
func TestSubagentFramesWhileParkedDoNotWake(t *testing.T) {
	root := fakeSDKInstallWithSource(t, "9.9.9", `
export function query({ prompt }) {
  const gen = (async function* () {
    const it = prompt[Symbol.asyncIterator]();
    const first = await it.next();
    yield { type: "system", subtype: "background_tasks_changed", tasks: [{ task_id: "t1" }] };
    yield { type: "result", subtype: "success", is_error: false, result: "armed", user_message_uuid: first.value.uuid, origin: first.value.origin };
    await new Promise((resolve) => setTimeout(resolve, 100));
    yield { type: "assistant", parent_tool_use_id: "toolu_task", message: { model: "m", content: [{ type: "text", text: "sub-agent progress" }] } };
    yield { type: "rate_limit_event", parent_tool_use_id: "toolu_task", rate_limit_info: { status: "allowed", rateLimitType: "five_hour", resetsAt: 9999999999 } };
    yield { type: "rate_limit_event", rate_limit_info: { status: "allowed", rateLimitType: "five_hour", resetsAt: 9999999999 } };
    await new Promise((resolve) => setTimeout(resolve, 200));
    yield { type: "system", subtype: "init", model: "m", session_id: "s", tools: [] };
    yield { type: "result", subtype: "success", is_error: false, result: "the sub-agent reported back", origin: { kind: "task-notification" } };
    yield { type: "system", subtype: "background_tasks_changed", tasks: [] };
    await new Promise(() => {});
  })();
  gen.interrupt = async () => ({});
  return gen;
}
`)
	withSearchDirs(t, []string{root}, root)
	backend := NewBackend()
	r := backend.(*runner)
	defer r.pool.closeAll()

	probe := newResidencyProbe(true)
	if _, err := runTurn(t, backend, "conv-subagent-wake", "arm it", t.TempDir(), probe.residency()); err != nil {
		t.Fatalf("first turn: %v", err)
	}

	ch := probe.wakeChannel(t, 0)
	deadline := time.After(10 * time.Second)
	var woke string
	for woke == "" {
		select {
		case evt := <-ch:
			if evt.Subagent || evt.Kind == agent.KindRateLimit {
				t.Fatalf("the wakeup turn began with a %s frame (subagent=%v), want the parent's own turn", evt.Kind, evt.Subagent)
			}
			if evt.Kind == agent.KindResult {
				woke = evt.Content
			}
		case <-deadline:
			t.Fatal("the provider-initiated turn never arrived")
		}
	}
	if woke != "the sub-agent reported back" {
		t.Fatalf("wakeup result = %q", woke)
	}
	time.Sleep(200 * time.Millisecond)
	if got := probe.wakeCount(); got != 1 {
		t.Fatalf("parked bridge woke %d times, want exactly 1 (only the parent's own turn)", got)
	}
}

// A background result can arrive while a user prompt is already waiting on
// the same resident query. Only the result correlated to that prompt may close
// its output channel; otherwise the later real answer is lost and the caller
// tears down or reparks the process too early.
func TestBackgroundResultDoesNotFinishPromptedTurn(t *testing.T) {
	root := fakeSDKInstallWithSource(t, "9.9.9", `
export function query({ prompt }) {
  const gen = (async function* () {
    let n = 0;
    for await (const message of prompt) {
      n += 1;
      yield { type: "system", subtype: "background_tasks_changed", tasks: [{ task_id: "t1" }] };
      if (n === 2) {
        yield { type: "result", subtype: "success", is_error: false, result: "background-result", user_message_uuid: message.uuid, origin: { kind: "task-notification" } };
        await new Promise((resolve) => setTimeout(resolve, 50));
      }
      yield { type: "result", subtype: "success", is_error: false, result: "reply-" + n + ":" + message.message.content, user_message_uuid: message.uuid, origin: message.origin };
    }
  })();
  gen.interrupt = async () => ({});
  return gen;
}
`)
	withSearchDirs(t, []string{root}, root)
	backend := NewBackend()
	r := backend.(*runner)
	defer r.pool.closeAll()

	probe := newResidencyProbe(true)
	workDir := t.TempDir()
	if _, err := runTurn(t, backend, "conv-correlated", "first", workDir, probe.residency()); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	events, err := runTurn(t, backend, "conv-correlated", "second", workDir, probe.residency())
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	want := []string{"background-result", "reply-2:second"}
	got := resultTexts(events)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("result sequence = %q, want %q", got, want)
	}
}

// Steering can race the result of the turn already using the resident query.
// Once the bridge accepts the new input, the existing sink must stay attached
// through the old result and close only on the steered message's result.
func TestSteerKeepsAttachedTurnThroughPreviousResult(t *testing.T) {
	root := fakeSDKInstallWithSource(t, "9.9.9", `
export function query({ prompt }) {
  const gen = (async function* () {
    const it = prompt[Symbol.asyncIterator]();
    const first = await it.next();
    yield { type: "system", subtype: "init", model: "m", session_id: "s", tools: [] };
    const second = await it.next();
    yield { type: "result", subtype: "success", is_error: false, result: "first-result", user_message_uuid: first.value.uuid, origin: first.value.origin };
    await new Promise((resolve) => setTimeout(resolve, 100));
    yield { type: "result", subtype: "success", is_error: false, result: "steered:" + second.value.message.content, user_message_uuid: second.value.uuid, origin: second.value.origin };
  })();
  gen.interrupt = async () => ({});
  return gen;
}
`)
	withSearchDirs(t, []string{root}, root)
	backend := NewBackend()
	r := backend.(*runner)
	defer r.pool.closeAll()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	outputCh := make(chan agent.StreamEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		errCh <- backend.RunWithSession(ctx, "first", t.TempDir(), agent.RunRequest{
			ControlID: "conv-steer-boundary",
			Residency: newResidencyProbe(true).residency(),
		}, outputCh)
	}()
	waitForActiveBridge(t, r, "conv-steer-boundary")
	var events []agent.StreamEvent
	select {
	case evt := <-outputCh:
		if evt.Kind != agent.KindSystemInit {
			t.Fatalf("first event = %v, want system init", evt.Kind)
		}
		events = append(events, evt)
	case <-ctx.Done():
		t.Fatal("SDK query did not become ready for steering")
	}

	steerer := backend.(agent.TurnSteeringBackend)
	if err := steerer.SteerTurn(ctx, "conv-steer-boundary", "steered-message", "focus on tests"); err != nil {
		t.Fatalf("SteerTurn: %v", err)
	}
	for evt := range outputCh {
		events = append(events, evt)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	want := []string{"first-result", "steered:focus on tests"}
	got := resultTexts(events)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("result sequence = %q, want %q", got, want)
	}
}

// A message steered into a turn the model is still working on is folded into
// that turn rather than queued behind it: the session absorbs it mid-turn and
// the whole exchange ends on ONE result, whose user_message_uuid is still the
// original prompt's. The folded id appears only in user_message_uuids. Binding
// the turn's end to the single uuid left the run waiting for a second result
// that never comes — the prompt stayed 'processing' and the UI span forever.
func TestFoldedSteerEndsTheTurnOnTheFirstResult(t *testing.T) {
	root := fakeSDKInstallWithSource(t, "9.9.9", `
export function query({ prompt }) {
  const gen = (async function* () {
    const it = prompt[Symbol.asyncIterator]();
    const first = await it.next();
    yield { type: "system", subtype: "init", model: "m", session_id: "s", tools: [] };
    const second = await it.next();
    yield {
      type: "result", subtype: "success", is_error: false,
      result: "folded:" + second.value.message.content,
      user_message_uuid: first.value.uuid,
      user_message_uuids: [first.value.uuid, second.value.uuid],
      origin: first.value.origin,
    };
    // A resident query does not end at a result: it parks on its input
    // stream. Without this the generator would return and the stream's EOF
    // would settle the turn, hiding the very stall under test.
    while (!(await it.next()).done) {}
  })();
  gen.interrupt = async () => ({});
  return gen;
}
`)
	withSearchDirs(t, []string{root}, root)
	backend := NewBackend()
	r := backend.(*runner)
	defer r.pool.closeAll()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	outputCh := make(chan agent.StreamEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		errCh <- backend.RunWithSession(ctx, "first", t.TempDir(), agent.RunRequest{
			ControlID: "conv-steer-folded",
			Residency: newResidencyProbe(true).residency(),
		}, outputCh)
	}()
	waitForActiveBridge(t, r, "conv-steer-folded")
	var events []agent.StreamEvent
	select {
	case evt := <-outputCh:
		if evt.Kind != agent.KindSystemInit {
			t.Fatalf("first event = %v, want system init", evt.Kind)
		}
		events = append(events, evt)
	case <-ctx.Done():
		t.Fatal("SDK query did not become ready for steering")
	}

	steerer := backend.(agent.TurnSteeringBackend)
	if err := steerer.SteerTurn(ctx, "conv-steer-folded", "steered-message", "also tag it"); err != nil {
		t.Fatalf("SteerTurn: %v", err)
	}
	for evt := range outputCh {
		events = append(events, evt)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	want := []string{"folded:also tag it"}
	got := resultTexts(events)
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("result sequence = %q, want %q", got, want)
	}
}

// Silently reusing a bridge whose settings no longer match would apply the
// old model to the new turn; silently retiring it would drop the background
// work without a word. Do the second, and say so.
func TestFingerprintMismatchRetiresAndSaysSo(t *testing.T) {
	root := residentSDK(t, 1)
	withSearchDirs(t, []string{root}, root)
	backend := NewBackend()
	r := backend.(*runner)
	defer r.pool.closeAll()

	probe := newResidencyProbe(true)
	workDir := t.TempDir()
	if _, err := runTurn(t, backend, "conv-fp", "first", workDir, probe.residency()); err != nil {
		t.Fatalf("first turn: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	outputCh := make(chan agent.StreamEvent, 64)
	errCh := make(chan error, 1)
	go func() {
		errCh <- backend.RunWithSession(ctx, "second", workDir, agent.RunRequest{
			ControlID: "conv-fp",
			Model:     "a-different-model",
			Residency: probe.residency(),
		}, outputCh)
	}()
	var events []agent.StreamEvent
	for evt := range outputCh {
		events = append(events, evt)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("second turn: %v", err)
	}
	// Back to reply-1: the settings change forced a fresh process.
	if got := resultText(events); got != "reply-1:second" {
		t.Fatalf("result = %q, want a freshly spawned process", got)
	}
	if !strings.Contains(probe.noticeText(), "settings changed") {
		t.Fatalf("notice does not explain the retirement: %q", probe.noticeText())
	}
}

func TestReclaimablePolicy(t *testing.T) {
	base := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		tasks       int
		parkedAt    time.Time
		emptyAt     time.Time
		lastEventAt time.Time
		attached    bool
		dead        bool
		now         time.Time
		want        bool
	}{
		"work still running": {tasks: 1, parkedAt: base, now: base.Add(10 * time.Minute), want: false},
		"silent work reaches event timeout": {
			tasks: 1, parkedAt: base, now: base.Add(backgroundEventSilenceTTL), want: true,
		},
		"recent event extends silence window": {
			tasks: 1, parkedAt: base, lastEventAt: base.Add(10 * time.Minute), now: base.Add(25 * time.Minute), want: false,
		},
		"events do not extend a park past the hard ttl": {
			tasks: 1, parkedAt: base, lastEventAt: base.Add(25 * time.Minute), now: base.Add(parkHardTTL + time.Minute), want: true,
		},
		"work finished, in grace": {tasks: 0, parkedAt: base, now: base.Add(parkDrainGrace / 2), want: false},
		"work finished, past grace": {
			tasks: 0, parkedAt: base, now: base.Add(parkDrainGrace + time.Second), want: true,
		},
		"grace starts when tasks empty, not when parked": {
			tasks: 0, parkedAt: base, emptyAt: base.Add(10 * time.Minute), now: base.Add(10*time.Minute + parkDrainGrace/2), want: false,
		},
		"wedged past the hard ttl": {
			tasks: 3, parkedAt: base, now: base.Add(parkHardTTL + time.Minute), want: true,
		},
		"a turn is attached": {tasks: 0, attached: true, parkedAt: base, now: base.Add(time.Hour), want: false},
		"process already gone": {
			tasks: 1, parkedAt: base, dead: true, now: base, want: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := &residentBridge{
				tasks: tc.tasks, tasksEmptyAt: tc.emptyAt, lastEventAt: tc.lastEventAt,
				parkedAt: tc.parkedAt, dead: tc.dead,
			}
			if tc.attached {
				b.sink = newBridgeSink(make(chan agent.StreamEvent, 1))
			}
			if got, _ := b.reclaimable(tc.now); got != tc.want {
				t.Fatalf("reclaimable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSilentBackgroundWorkIsStoppedAndNotified(t *testing.T) {
	root := residentSDK(t, 1)
	withSearchDirs(t, []string{root}, root)
	backend := NewBackend()
	r := backend.(*runner)
	defer r.pool.closeAll()

	probe := newResidencyProbe(true)
	if _, err := runTurn(t, backend, "conv-silent", "go", t.TempDir(), probe.residency()); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}

	r.pool.mu.Lock()
	b := r.pool.bridges["conv-silent"]
	r.pool.mu.Unlock()
	if b == nil {
		t.Fatal("resident bridge was not parked")
	}
	b.mu.Lock()
	lastEventAt := b.lastEventAt
	b.mu.Unlock()

	r.pool.mu.Lock()
	r.pool.reclaimLocked(lastEventAt.Add(backgroundEventSilenceTTL), "")
	r.pool.mu.Unlock()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, releases, notices := probe.counts()
		if releases == 1 && notices == 1 {
			if got := probe.noticeText(); !strings.Contains(got, "no events for 30 minutes") {
				t.Fatalf("notice = %q, want the 30-minute event timeout", got)
			}
			if n := r.pool.parkedCount(); n != 0 {
				t.Fatalf("parked bridges = %d, want 0 after silence timeout", n)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, releases, notices := probe.counts()
	t.Fatalf("silence cleanup releases=%d notices=%d, want 1/1", releases, notices)
}

// A bridge that has never parked (parkedAt zero) must not be swept: it is
// mid-handover, not idle.
func TestReclaimableIgnoresNeverParkedBridges(t *testing.T) {
	b := &residentBridge{tasks: 0}
	if got, why := b.reclaimable(time.Now()); got {
		t.Fatalf("a never-parked bridge was reclaimed: %s", why)
	}
}

// What the drainer reads before a graceful upgrade decides the server is idle.
// It counts bridges whose work an upgrade would visibly destroy — not every
// parked process, or a wedged bridge could postpone upgrades forever.
func TestActiveResidentCountBoundsWhatCanPostponeAnUpgrade(t *testing.T) {
	base := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	restore := nowFunc
	nowFunc = func() time.Time { return base }
	defer func() { nowFunc = restore }()

	pool := newBridgePool()
	pool.bridges["fresh"] = &residentBridge{key: "fresh", tasks: 1, parkedAt: base.Add(-time.Minute)}
	pool.bridges["wedged"] = &residentBridge{key: "wedged", tasks: 1, parkedAt: base.Add(-parkBlocksUpgradeFor - time.Minute)}
	pool.bridges["drained"] = &residentBridge{key: "drained", tasks: 0, parkedAt: base.Add(-time.Minute)}
	pool.bridges["handover"] = &residentBridge{key: "handover", tasks: 1}

	if got := pool.activeResidentCount(); got != 1 {
		t.Fatalf("activeResidentCount = %d, want 1 (only the recently-parked bridge with live work)", got)
	}
	if got := pool.parkedCount(); got != 4 {
		t.Fatalf("parkedCount = %d, want 4 — the upgrade bound must not change what is parked", got)
	}
}

func TestCloseAllRetiresTheWholeFleet(t *testing.T) {
	root := residentSDK(t, 1)
	withSearchDirs(t, []string{root}, root)
	backend := NewBackend()
	r := backend.(*runner)

	probe := newResidencyProbe(true)
	for _, conv := range []string{"conv-a", "conv-b"} {
		if _, err := runTurn(t, backend, conv, "go", t.TempDir(), probe.residency()); err != nil {
			t.Fatalf("%s: %v", conv, err)
		}
	}
	if n := r.pool.parkedCount(); n != 2 {
		t.Fatalf("parked bridges = %d, want 2", n)
	}
	r.pool.closeAll()
	if n := r.pool.parkedCount(); n != 0 {
		t.Fatalf("parked bridges after closeAll = %d, want 0", n)
	}
	if _, releases, _ := probe.counts(); releases != 2 {
		t.Fatalf("releases = %d, want 2 — a retired bridge must free its slot", releases)
	}
	// Shut down: a later park must be refused rather than resurrect the pool.
	if r.pool.put(&residentBridge{key: "conv-c"}) {
		t.Fatal("a shut-down pool accepted a new bridge")
	}
}

func TestTakingADeadBridgeStillReapsAndReleasesIt(t *testing.T) {
	root := residentSDK(t, 1)
	withSearchDirs(t, []string{root}, root)
	backend := NewBackend()
	r := backend.(*runner)
	defer r.pool.closeAll()

	probe := newResidencyProbe(true)
	if _, err := runTurn(t, backend, "conv-dead", "go", t.TempDir(), probe.residency()); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	r.pool.mu.Lock()
	b := r.pool.bridges["conv-dead"]
	r.pool.mu.Unlock()
	if b == nil {
		t.Fatal("resident bridge was not parked")
	}
	b.mu.Lock()
	b.dead = true
	b.mu.Unlock()
	if got, _ := r.pool.take("conv-dead", b.fingerprint); got != nil {
		t.Fatal("a dead bridge was returned for reuse")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, releases, notices := probe.counts()
		if releases == 1 && notices == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, releases, notices := probe.counts()
	t.Fatalf("dead bridge cleanup releases=%d notices=%d, want 1/1", releases, notices)
}

// A side run (oneshot, title, compact) has no conversation to park against and
// must keep exiting at `result`.
func TestSideRunsNeverPark(t *testing.T) {
	root := residentSDK(t, 1)
	withSearchDirs(t, []string{root}, root)
	backend := NewBackend()
	r := backend.(*runner)
	defer r.pool.closeAll()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := backend.RunOneshot(ctx, "summarise", t.TempDir(), agent.RunRequest{}); err != nil {
		t.Fatalf("RunOneshot: %v", err)
	}
	if n := r.pool.parkedCount(); n != 0 {
		t.Fatalf("a oneshot parked %d bridge(s)", n)
	}
}

func TestAttachRefusesASecondConcurrentTurn(t *testing.T) {
	b := &residentBridge{}
	if _, err := b.attach(make(chan agent.StreamEvent, 1), "turn-1"); err != nil {
		t.Fatalf("first attach: %v", err)
	}
	if _, err := b.attach(make(chan agent.StreamEvent, 1), "turn-2"); err == nil {
		t.Fatal("a second turn attached to a bridge that already had one")
	}
	b.dead = true
	b.sink = nil
	if _, err := b.attach(make(chan agent.StreamEvent, 1), "turn-3"); !errors.Is(err, errBridgeRetired) {
		t.Fatalf("attach to a dead bridge = %v, want errBridgeRetired", err)
	}
}

// Regression: an abandoned wakeup consumer filled its event buffer, then the
// router blocked sending the next frame while holding residentBridge.mu.
// bridgePool.put held the fleet-wide mutex while asking that bridge whether it
// was reclaimable, so one conversation stopped every Claude conversation.
func TestBlockedSinkDoesNotBlockResidentPoolMaintenance(t *testing.T) {
	blocked := &residentBridge{
		key:      "blocked-conversation",
		parkedAt: time.Now(),
	}
	sink := make(chan agent.StreamEvent, 1)
	sink <- agent.StreamEvent{Kind: agent.KindDelta, Content: "fill the consumer buffer"}
	if _, err := blocked.attach(sink, "turn-1"); err != nil {
		t.Fatalf("attach blocked bridge: %v", err)
	}

	forwarded := make(chan struct{})
	go func() {
		blocked.forward(agent.StreamEvent{Kind: agent.KindDelta, Content: "would block"}, false)
		close(forwarded)
	}()
	// The pre-filled sink makes forward block deterministically once scheduled.
	// Give it the CPU, then verify it has not returned before maintenance starts.
	time.Sleep(20 * time.Millisecond)
	select {
	case <-forwarded:
		t.Fatal("forward unexpectedly completed against a full sink")
	default:
	}

	pool := newBridgePool()
	pool.bridges[blocked.key] = blocked
	putDone := make(chan bool, 1)
	go func() {
		putDone <- pool.put(&residentBridge{key: "other-conversation", parkedAt: time.Now()})
	}()
	select {
	case accepted := <-putDone:
		if !accepted {
			t.Fatal("open resident pool rejected another conversation")
		}
	case <-time.After(time.Second):
		t.Fatal("one blocked sink stalled resident pool maintenance for other conversations")
	}

	blocked.detach(context.Canceled)
	select {
	case <-forwarded:
	case <-time.After(time.Second):
		t.Fatal("detach did not release the blocked sink sender")
	}
}
