package service

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/agenttest"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

type runtimeTestSandbox struct{}

func (runtimeTestSandbox) Wrap(argv []string, _ string, _ agent.WrapOpts) ([]string, []string, error) {
	return argv, nil, nil
}

type runtimeTestObserver struct{}

func (runtimeTestObserver) ObservePrompt(context.Context, store.Message) PromptObservation {
	return nil
}

// signallingBackend reports each prompt it runs on a channel, so a test can
// wait for the dispatcher → runtime → backend hop without sharing mutable state
// with the worker goroutine.
type signallingBackend struct {
	agenttest.ScriptedBackend
	ran chan string
}

func (b signallingBackend) RunWithSession(ctx context.Context, prompt, wd string, opts agent.RunRequest, ch chan<- agent.StreamEvent) error {
	b.ran <- prompt
	return b.ScriptedBackend.RunWithSession(ctx, prompt, wd, opts, ch)
}

func fullyWiredRuntime(t *testing.T) *Runtime {
	t.Helper()
	cfg := agenttest.Config()
	rt := NewRuntime(storetest.New(), NewBackendRegistry(nil, agenttest.ScriptedBackend{}))
	rt.Cfg = cfg
	rt.Drainer = NewDrainer()
	rt.Pool = NewPool(cfg)
	rt.Sandbox = runtimeTestSandbox{}
	rt.TitleGen = &agenttest.FakeTitler{}
	rt.TitleGens = map[string]agent.TitleGenerator{}
	rt.AutoTitleDelay = time.Second
	rt.BarkSender = NewHTTPBarkSender()
	rt.PushDeerSender = NewHTTPPushDeerSender()
	rt.UsageLoc = time.UTC
	rt.PromptObserver = runtimeTestObserver{}
	return rt
}

func TestRuntimeValidateNamesEveryUnwiredCollaborator(t *testing.T) {
	rt := NewRuntime(storetest.New(), NewBackendRegistry(nil, nil))
	err := rt.Validate()
	if err == nil {
		t.Fatal("a runtime with no pool, drainer, config, ... validated")
	}
	for _, want := range []string{"Cfg", "Drainer", "Pool", "TitleGen", "TitleGens", "BarkSender", "PushDeerSender", "UsageLoc", "PromptObserver"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate error %q does not name %s", err, want)
		}
	}
	// Owned by NewRuntime, and the sandbox may legitimately be absent.
	for _, notWant := range []string{"Store", "Broadcaster", "UserHub", "Pause", "Dispatcher", "Sandbox"} {
		if strings.Contains(err.Error(), notWant+",") || strings.HasSuffix(err.Error(), notWant) {
			t.Errorf("Validate error %q names %s, which is wired or optional", err, notWant)
		}
	}
}

func TestRuntimeValidateAcceptsAFullyWiredRuntime(t *testing.T) {
	rt := fullyWiredRuntime(t)
	if err := rt.Validate(); err != nil {
		t.Fatal(err)
	}
	rt.Sandbox = nil
	if err := rt.Validate(); err != nil {
		t.Fatalf("sandbox is optional (disabled, or unavailable on the host): %v", err)
	}
}

// assertNoNilFields fails for every nil-able field of v that is nil and not in
// allowed. Used to prove the Runtime's assemblers copy every collaborator: a
// field added to PromptRunner or MessagePersister but not to the assembler
// shows up here instead of as a silent nil in production.
func assertNoNilFields(t *testing.T, v any, allowed ...string) {
	t.Helper()
	rv := reflect.ValueOf(v).Elem()
	skip := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		skip[name] = true
	}
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Type().Field(i)
		if skip[f.Name] || !f.IsExported() {
			continue
		}
		switch fv := rv.Field(i); fv.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Func, reflect.Slice, reflect.Chan:
			if fv.IsNil() {
				t.Errorf("%s.%s is nil although the runtime has every collaborator wired", rv.Type().Name(), f.Name)
			}
		}
	}
}

func TestRuntimeAssemblersCopyEveryCollaborator(t *testing.T) {
	rt := fullyWiredRuntime(t)
	assertNoNilFields(t, rt.PromptRunner())
	assertNoNilFields(t, rt.PromptIntake())
	assertNoNilFields(t, rt.TurnAdmission())
	// TitleLimiter is a per-instance test override; nil selects the
	// process-wide limiter by design.
	persister := rt.MessagePersister()
	assertNoNilFields(t, persister, "TitleLimiter")
	if persister.AutoTitleDelay != rt.AutoTitleDelay {
		t.Errorf("AutoTitleDelay = %v, want %v", persister.AutoTitleDelay, rt.AutoTitleDelay)
	}
}

// The dispatcher NewRuntime builds runs prompts through the runtime itself —
// the path cron submissions take — with no handler involved.
func TestRuntimeDispatcherRunsPromptsThroughTheRegistry(t *testing.T) {
	ms := storetest.New()
	ctx := context.Background()
	_ = ms.CreateUser(ctx, store.User{ID: "u1", Username: "alice", Email: "a@example.com", WorkDir: t.TempDir()})
	_ = ms.CreateConversation(ctx, "c1", "", "u1", t.TempDir(), config.CLITypeCodex, "")
	ran := make(chan string, 1)
	codex := signallingBackend{ScriptedBackend: agenttest.ScriptedBackend{Result: "done"}, ran: ran}
	fallback := signallingBackend{ScriptedBackend: agenttest.ScriptedBackend{Result: "wrong"}, ran: make(chan string, 1)}
	rt := NewRuntime(ms, NewBackendRegistry(map[string]agent.Backend{config.CLITypeCodex: codex}, fallback))
	if _, _, _, err := rt.Dispatcher.Start(ctx); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(rt.Dispatcher.Stop)

	if _, err := rt.Dispatcher.Enqueue(ctx, "c1", "scheduled hello"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case prompt := <-ran:
		if !strings.Contains(prompt, "scheduled hello") {
			t.Fatalf("backend ran %q, want the enqueued prompt", prompt)
		}
	case <-fallback.ran:
		t.Fatal("a codex conversation ran on the fallback backend")
	case <-time.After(5 * time.Second):
		t.Fatal("the enqueued prompt never reached the codex backend")
	}
}

// A prompt picked up during a drain must stay pending for the next process:
// ProcessPrompt parks until the dispatcher shuts down and never runs it.
func TestRuntimeProcessPromptParksWhileDraining(t *testing.T) {
	ran := make(chan string, 1)
	rt := NewRuntime(storetest.New(), NewBackendRegistry(nil, signallingBackend{ran: ran}))
	rt.Drainer = NewDrainer()
	rt.Drainer.StartDrain()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		rt.ProcessPrompt(ctx, store.Message{ID: "m1", ConversationID: "c1", Content: "hi"})
	}()
	select {
	case <-done:
		t.Fatal("ProcessPrompt returned while draining instead of parking")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ProcessPrompt did not return once the dispatcher context ended")
	}
	select {
	case <-ran:
		t.Fatal("a drained prompt reached the backend")
	default:
	}
}
