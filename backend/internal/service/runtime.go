package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// Runtime is the process-wide set of collaborators shared by every entry point
// that can start, observe, or persist an agent turn: the web chat WebSocket,
// /compact, the cron scheduler, and the IM bridge.
//
// It exists so that each of those consumers holds one pointer instead of its
// own copy of a dozen fields. Before it, the terminal handler owned these
// instances, every other consumer borrowed them through field-by-field copies,
// and a later assembly step backfilled handlers that had been built too early.
// Adding a collaborator meant finding every copy; missing one compiled fine and
// produced a nil that the many nil-tolerant branches then silently swallowed.
// Now a field is wired once, in the production assembly, and Validate refuses
// to start a server where any required field is still nil.
//
// Fields are exported so focused tests can build a partial Runtime literal;
// production goes through NewRuntime and Validate.
type Runtime struct {
	Store store.Store
	Cfg   *config.Config
	// Backends resolves a conversation's provider to the agent backend that
	// runs it.
	Backends *BackendRegistry

	Broadcaster *Broadcaster
	// UserHub fans user-scoped events (conversation list lifecycle, auto
	// title) out to every WebSocket the user has open.
	UserHub *UserHub
	// Drainer tracks in-flight jobs for graceful shutdown.
	Drainer *Drainer
	// Pause is the operator's manual hold on new work, shared by every entry
	// point that can start one so a single switch covers all of them.
	Pause *PauseGate
	// Pool is the per-account concurrency limiter.
	Pool *Pool
	// GuardrailReminders carries a completed task's soft reminder to the next
	// user message without ever parking the active run.
	GuardrailReminders *GuardrailReminderTracker
	// Sandbox wraps spawned agent processes. Legitimately nil when sandboxing
	// is disabled, or when the requested implementation is unavailable on the
	// host (startup logs that loudly and degrades instead of refusing to run).
	Sandbox agent.Sandbox `runtime:"optional"`
	// Dispatcher serializes queued prompts per conversation. Built by
	// NewRuntime with this Runtime's ProcessPrompt as its processor, so web
	// chat and cron submissions run through the service layer directly.
	Dispatcher *Dispatcher

	// TitleGen is the default auto-title generator; TitleGens overrides it
	// per provider.
	TitleGen  agent.TitleGenerator
	TitleGens map[string]agent.TitleGenerator
	// AutoTitleDelay staggers the title CLI behind the main turn so the two
	// don't rewrite the account's OAuth token file at the same instant. Zero
	// (the test default) runs the generator immediately.
	AutoTitleDelay time.Duration
	BarkSender     BarkSender
	PushDeerSender PushDeerSender
	// UsageLoc buckets token_usage rows by day.
	UsageLoc *time.Location
	// PromptObserver mirrors web-chat turns to an attached transport (the IM
	// thread that created the conversation).
	PromptObserver PromptObserver
}

// NewRuntime builds a Runtime with the instances it owns outright — the
// broadcaster, user hub, pause gate and (when a store is present) the prompt
// dispatcher — already wired to each other. The remaining fields are supplied
// by the caller: production assembly sets all of them and then calls Validate.
func NewRuntime(s store.Store, backends *BackendRegistry) *Runtime {
	rt := &Runtime{
		Store:              s,
		Backends:           backends,
		Broadcaster:        NewBroadcaster(),
		UserHub:            NewUserHub(),
		Pause:              NewPauseGate(),
		GuardrailReminders: NewGuardrailReminderTracker(),
	}
	if s != nil {
		// The processor resolves every collaborator from rt at call time, so a
		// field assigned after construction (production assembly, or a test
		// swapping in a fake) is still what the next prompt sees.
		rt.Dispatcher = NewDispatcher(s, rt.ProcessPrompt)
		rt.Dispatcher.SetPauseGate(rt.Pause)
	}
	return rt
}

// Validate reports every field production needs that is still unset. It walks
// the struct by reflection on purpose: a newly added collaborator is required
// by default, so forgetting to wire it fails startup instead of degrading into
// a nil-tolerant no-op. Fields that may legitimately be nil opt out with the
// `runtime:"optional"` tag.
func (rt *Runtime) Validate() error {
	if rt == nil {
		return errors.New("runtime: nil")
	}
	v := reflect.ValueOf(rt).Elem()
	t := v.Type()
	var missing []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Tag.Get("runtime") == "optional" {
			continue
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Func, reflect.Slice, reflect.Chan:
			if fv.IsNil() {
				missing = append(missing, f.Name)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("runtime: unwired collaborators: %s", strings.Join(missing, ", "))
	}
	return nil
}

// ProcessPrompt is the dispatcher's per-prompt callback. A prompt picked up
// while the server drains is parked until the dispatcher shuts down, so it
// stays 'pending' and the next process resumes it.
func (rt *Runtime) ProcessPrompt(dispatcherCtx context.Context, prompt store.Message) {
	if rt.Drainer.IsDraining() {
		<-dispatcherCtx.Done()
		return
	}
	rt.PromptRunner().ProcessPrompt(dispatcherCtx, prompt)
}

// PromptRunner assembles the per-turn orchestrator from the runtime. Built per
// call so it always reflects the current fields.
func (rt *Runtime) PromptRunner() *PromptRunner {
	return &PromptRunner{
		Store:              rt.Store,
		Broadcaster:        rt.Broadcaster,
		UserHub:            rt.UserHub,
		Drainer:            rt.Drainer,
		Pause:              rt.Pause,
		Pool:               rt.Pool,
		Dispatcher:         rt.Dispatcher,
		Sandbox:            rt.Sandbox,
		Cfg:                rt.Cfg,
		Persist:            rt.MessagePersister(),
		GuardrailReminders: rt.GuardrailReminders,
		Backends:           rt.Backends,
		Observer:           rt.PromptObserver,
	}
}

// TurnAdmission assembles the shared turn admission (room claim, drain and
// pause gates, account slot, activity broadcast) from the runtime.
func (rt *Runtime) TurnAdmission() *TurnAdmission {
	return &TurnAdmission{
		Broadcaster: rt.Broadcaster,
		Drainer:     rt.Drainer,
		Pause:       rt.Pause,
		Pool:        rt.Pool,
		UserHub:     rt.UserHub,
		Store:       rt.Store,
	}
}

// PromptIntake assembles the web-chat submission path from the runtime.
func (rt *Runtime) PromptIntake() *PromptIntake {
	return &PromptIntake{
		Store:       rt.Store,
		Dispatcher:  rt.Dispatcher,
		Broadcaster: rt.Broadcaster,
		UserHub:     rt.UserHub,
		Drainer:     rt.Drainer,
		Pause:       rt.Pause,
		Persist:     rt.MessagePersister(),
	}
}

// MessagePersister assembles the persistence/notification side of a turn from
// the runtime. Built per call for the same reason as PromptRunner.
func (rt *Runtime) MessagePersister() *MessagePersister {
	return &MessagePersister{
		Store:          rt.Store,
		Broadcaster:    rt.Broadcaster,
		UserHub:        rt.UserHub,
		Pool:           rt.Pool,
		BarkSender:     rt.BarkSender,
		PushDeerSender: rt.PushDeerSender,
		TitleGen:       rt.TitleGen,
		TitleGens:      rt.TitleGens,
		AutoTitleDelay: rt.AutoTitleDelay,
		UsageLoc:       rt.UsageLoc,
		Cfg:            rt.Cfg,
	}
}
