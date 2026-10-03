package claudeagentsdk

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// A Stop must reach the SDK as query.interrupt({cancel_queued:true}) rather
// than as a signal to the process group: on a resident bridge the process has
// to survive the interrupt, and "stop" has to mean the queued wakeups stop too.
func TestInterruptSendsCancelQueuedAndIsAcked(t *testing.T) {
	root := fakeSDKInstallWithSource(t, "9.9.9", `
export function query({ prompt }) {
  let fire;
  const interrupted = new Promise((resolve) => { fire = resolve; });
  const gen = (async function* () {
    const it = prompt[Symbol.asyncIterator]();
    await it.next();
    const opts = await interrupted;
    yield {
      type: "result",
      subtype: "success",
      is_error: false,
      result: "interrupted:" + JSON.stringify(opts),
    };
  })();
  gen.interrupt = async (opts) => { fire(opts ?? null); return {}; };
  return gen;
}
`)
	withSearchDirs(t, []string{root}, root)

	backend := NewBackend()
	r := backend.(*runner)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	outputCh := make(chan agent.StreamEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		errCh <- backend.RunWithSession(ctx, "first", t.TempDir(), agent.RunRequest{
			ControlID: "conv-interrupt",
		}, outputCh)
	}()
	waitForActiveBridge(t, r, "conv-interrupt")
	bridge := activeBridgeFor(t, r, "conv-interrupt")

	if err := bridge.interrupt(ctx, "stop-1"); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}

	var result string
	for event := range outputCh {
		if event.Kind == agent.KindResult {
			result = event.Content
		}
	}
	if !strings.Contains(result, `"cancel_queued":true`) {
		t.Fatalf("interrupt did not carry cancel_queued, result = %q", result)
	}
}

// Without interrupt_cancel_queued_v1 the SDK rejects the option; the bridge
// must retry bare rather than give up, because a plain interrupt still stops
// the running turn — losing only the queued-wakeup half of the guarantee.
func TestInterruptFallsBackWhenCancelQueuedUnsupported(t *testing.T) {
	root := fakeSDKInstallWithSource(t, "9.9.9", `
export function query({ prompt }) {
  let fire;
  const interrupted = new Promise((resolve) => { fire = resolve; });
  const gen = (async function* () {
    const it = prompt[Symbol.asyncIterator]();
    await it.next();
    const how = await interrupted;
    yield { type: "result", subtype: "success", is_error: false, result: "stopped:" + how };
  })();
  gen.interrupt = async (opts) => {
    if (opts && opts.cancel_queued) throw new Error("unknown option cancel_queued");
    fire("bare");
    return {};
  };
  return gen;
}
`)
	withSearchDirs(t, []string{root}, root)

	backend := NewBackend()
	r := backend.(*runner)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	outputCh := make(chan agent.StreamEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		errCh <- backend.RunWithSession(ctx, "first", t.TempDir(), agent.RunRequest{
			ControlID: "conv-interrupt-fallback",
		}, outputCh)
	}()
	waitForActiveBridge(t, r, "conv-interrupt-fallback")
	bridge := activeBridgeFor(t, r, "conv-interrupt-fallback")

	if err := bridge.interrupt(ctx, "stop-1"); err != nil {
		t.Fatalf("interrupt should have succeeded on the bare retry: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}

	var result string
	for event := range outputCh {
		if event.Kind == agent.KindResult {
			result = event.Content
		}
	}
	if result != "stopped:bare" {
		t.Fatalf("result = %q, want the bare-interrupt path", result)
	}
}

// acknowledge derives its pending-map key from the action. An ack it does not
// recognise must be dropped, not folded into the input namespace, or a future
// bridge frame would silently resolve an unrelated in-flight steer.
func TestAcknowledgeIgnoresUnknownActions(t *testing.T) {
	a := newActiveBridge(nopWriteCloser{})
	ack := make(chan error, 1)
	a.pending["input:m1"] = ack

	a.acknowledge(bridgeControlFrame{Type: bridgeControlType, Action: "something_ack", MessageID: "m1", Accepted: true})
	select {
	case err := <-ack:
		t.Fatalf("unknown ack resolved a pending steer: %v", err)
	default:
	}

	a.acknowledge(bridgeControlFrame{Type: bridgeControlType, Action: "input_ack", MessageID: "m1", Accepted: true})
	select {
	case err := <-ack:
		if err != nil {
			t.Fatalf("input_ack: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("input_ack did not resolve its pending steer")
	}
}

func activeBridgeFor(t *testing.T, r *runner, controlID string) *activeBridge {
	t.Helper()
	r.activeMu.RLock()
	defer r.activeMu.RUnlock()
	bridge := r.active[controlID]
	if bridge == nil {
		t.Fatalf("no active bridge registered for %q", controlID)
	}
	return bridge
}
