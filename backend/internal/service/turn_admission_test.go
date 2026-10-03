package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/config"
)

const admitTestAccount = "acc1"

func newAdmitTestAdmission(t *testing.T, maxConcurrent int) *TurnAdmission {
	t.Helper()
	return &TurnAdmission{
		Broadcaster: NewBroadcaster(),
		Drainer:     NewDrainer(),
		Pause:       NewPauseGate(),
		Pool: NewPool(&config.Config{Providers: []config.Provider{{
			Name: admitTestAccount, Type: config.CLITypeClaude, MaxConcurrent: maxConcurrent,
		}}}),
	}
}

func admitTestSpec(convID string) AdmitSpec {
	return AdmitSpec{
		ConversationID: convID,
		Target: TurnTarget{
			Job:         DrainerJob{ConversationID: convID, AccountName: admitTestAccount},
			PoolUserID:  "u1",
			AccountName: admitTestAccount,
			Model:       "m",
		},
	}
}

func holdAdmitSlot(t *testing.T, a *TurnAdmission) {
	t.Helper()
	holder, err := a.Pool.EnterForUser("", admitTestAccount, "m")
	if err != nil {
		t.Fatalf("enter holder: %v", err)
	}
	if err := holder.Wait(context.Background()); err != nil {
		t.Fatalf("wait holder: %v", err)
	}
	t.Cleanup(holder.Release)
}

func assertAdmissionEmpty(t *testing.T, a *TurnAdmission, convID string) {
	t.Helper()
	if a.Broadcaster.IsBusy(convID) {
		t.Error("room still held")
	}
	if n := a.Drainer.InFlight(); n != 0 {
		t.Errorf("drainer in-flight = %d, want 0", n)
	}
}

// The hooks fire in the order the transports depend on, and the drainer entry
// moves queued → running only once the slot is held.
func TestAdmitRunsTheSequenceInOrder(t *testing.T) {
	a := newAdmitTestAdmission(t, 1)
	var steps []string
	var mu sync.Mutex
	record := func(step string) {
		mu.Lock()
		steps = append(steps, step)
		mu.Unlock()
	}
	spec := admitTestSpec("c1")
	spec.OnRoomClaimed = func() {
		if !a.Broadcaster.IsBusy("c1") {
			t.Error("OnRoomClaimed ran before the room was held")
		}
		if a.Drainer.InFlight() != 0 {
			t.Error("OnRoomClaimed ran after the drainer entry was registered")
		}
		record("room")
	}
	spec.OnTicket = func(_ *Ticket, requeue bool) {
		jobs := a.Drainer.Jobs()
		if len(jobs) != 1 || jobs[0].Status != JobStatusQueued {
			t.Errorf("drainer jobs at ticket time = %+v, want one queued", jobs)
		}
		if requeue {
			t.Error("first ticket reported as a requeue")
		}
		record("ticket")
	}
	spec.OnGranted = func() {
		if jobs := a.Drainer.Jobs(); jobs[0].Status != JobStatusQueued {
			t.Errorf("status at grant = %q, want still queued", jobs[0].Status)
		}
		record("granted")
	}

	lease, err := a.Admit(spec)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if got := strings.Join(steps, ","); got != "room,ticket,granted" {
		t.Fatalf("hook order = %s", got)
	}
	if jobs := a.Drainer.Jobs(); len(jobs) != 1 || jobs[0].Status != JobStatusRunning {
		t.Fatalf("drainer jobs = %+v, want one running", jobs)
	}
	if inUse, _, _ := a.Pool.Stats(admitTestAccount); inUse != 1 {
		t.Fatalf("pool inUse = %d, want 1", inUse)
	}

	lease.Release()
	lease.Release() // idempotent
	assertAdmissionEmpty(t, a, "c1")
	if inUse, _, _ := a.Pool.Stats(admitTestAccount); inUse != 0 {
		t.Fatalf("pool inUse = %d after release, want 0", inUse)
	}
}

func TestAdmitRefusals(t *testing.T) {
	tests := []struct {
		name     string
		arrange  func(t *testing.T, a *TurnAdmission, spec *AdmitSpec)
		wantIs   error
		wantAs   func(error) bool
		wantRoom bool // OnRefused expected (the room had been claimed)
	}{
		{
			name:    "paused",
			arrange: func(_ *testing.T, a *TurnAdmission, _ *AdmitSpec) { a.Pause.SetPaused(true) },
			wantIs:  ErrTurnPaused,
		},
		{
			name: "busy room",
			arrange: func(t *testing.T, a *TurnAdmission, _ *AdmitSpec) {
				a.Broadcaster.StartJob("c1", func() {})
				t.Cleanup(func() { a.Broadcaster.EndJob("c1") })
			},
			wantIs: ErrTurnBusy,
		},
		{
			name:     "draining",
			arrange:  func(_ *testing.T, a *TurnAdmission, _ *AdmitSpec) { a.Drainer.StartDrain(DrainReasonUpgrade) },
			wantIs:   ErrTurnDraining,
			wantRoom: true,
		},
		{
			name: "cooldown",
			arrange: func(_ *testing.T, a *TurnAdmission, _ *AdmitSpec) {
				a.Pool.Cooldown(admitTestAccount, "m", time.Now().Add(time.Hour))
			},
			wantAs: func(err error) bool {
				var cd *CooldownError
				var entry *PoolEntryError
				return errors.As(err, &cd) && errors.As(err, &entry)
			},
			wantRoom: true,
		},
		{
			name: "queue timeout",
			arrange: func(t *testing.T, a *TurnAdmission, spec *AdmitSpec) {
				holdAdmitSlot(t, a)
				spec.QueueTimeout = 20 * time.Millisecond
			},
			wantAs: func(err error) bool {
				var timeout *QueueTimeoutError
				return errors.As(err, &timeout) && !errors.Is(err, context.Canceled)
			},
			wantRoom: true,
		},
		{
			name: "caller cancel while queued",
			arrange: func(t *testing.T, a *TurnAdmission, spec *AdmitSpec) {
				holdAdmitSlot(t, a)
				ctx, cancel := context.WithCancel(context.Background())
				spec.QueueCtx = ctx
				spec.QueueTimeout = time.Minute
				spec.OnTicket = func(*Ticket, bool) { cancel() }
			},
			wantIs:   context.Canceled,
			wantRoom: true,
		},
		{
			name: "resolve fails",
			arrange: func(_ *testing.T, _ *TurnAdmission, spec *AdmitSpec) {
				spec.Resolve = func() (TurnTarget, error) { return TurnTarget{}, errResolveTest }
			},
			wantIs:   errResolveTest,
			wantRoom: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newAdmitTestAdmission(t, 1)
			spec := admitTestSpec("c1")
			refused := 0
			spec.OnRefused = func(error) {
				if !a.Broadcaster.IsBusy("c1") {
					t.Error("OnRefused ran after the room was handed back")
				}
				refused++
			}
			tt.arrange(t, a, &spec)

			lease, err := a.Admit(spec)
			if lease != nil {
				t.Fatal("a refused admission returned a lease")
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Fatalf("err = %v, want %v", err, tt.wantIs)
			}
			if tt.wantAs != nil && !tt.wantAs(err) {
				t.Fatalf("err = %v (%T) has the wrong type", err, err)
			}
			if want := map[bool]int{true: 1, false: 0}[tt.wantRoom]; refused != want {
				t.Fatalf("OnRefused calls = %d, want %d", refused, want)
			}
			if tt.name != "busy room" {
				assertAdmissionEmpty(t, a, "c1")
			}
			if _, queued, _ := a.Pool.Stats(admitTestAccount); queued != 0 {
				t.Fatalf("pool queued = %d, want the ticket gone", queued)
			}
		})
	}
}

var errResolveTest = errors.New("resolve failed")

// Describe rewrites both the returned error and what OnRefused sees.
func TestAdmitDescribeAppliesToRefusals(t *testing.T) {
	a := newAdmitTestAdmission(t, 1)
	a.Drainer.StartDrain(DrainReasonUpgrade)
	spec := admitTestSpec("c1")
	spec.Describe = func(err error) error { return errors.New("described: " + err.Error()) }
	var seen error
	spec.OnRefused = func(err error) { seen = err }

	_, err := a.Admit(spec)
	if err == nil || !strings.HasPrefix(err.Error(), "described: ") {
		t.Fatalf("err = %v, want the described form", err)
	}
	if seen == nil || seen.Error() != err.Error() {
		t.Fatalf("OnRefused saw %v, want %v", seen, err)
	}
}

// WaitForRoom queues behind the current owner instead of refusing, and a
// cancelled wait reports ErrTurnBusy with the context's reason.
func TestAdmitWaitsForTheRoom(t *testing.T) {
	a := newAdmitTestAdmission(t, 1)
	a.Broadcaster.StartJob("c1", func() {})
	spec := admitTestSpec("c1")
	spec.WaitForRoom = context.Background()

	done := make(chan error, 1)
	go func() {
		lease, err := a.Admit(spec)
		if err == nil {
			lease.Release()
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Admit returned while the room was held: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	a.Broadcaster.EndJob("c1")
	if err := <-done; err != nil {
		t.Fatalf("Admit after the room freed: %v", err)
	}

	a.Broadcaster.StartJob("c1", func() {})
	defer a.Broadcaster.EndJob("c1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	spec.WaitForRoom = ctx
	_, err := a.Admit(spec)
	if !errors.Is(err, ErrTurnBusy) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want ErrTurnBusy wrapping context.Canceled", err)
	}
}

// PauseHeldUpstream lets a caller whose paused work waits elsewhere through.
func TestAdmitPauseHeldUpstream(t *testing.T) {
	a := newAdmitTestAdmission(t, 1)
	a.Pause.SetPaused(true)
	spec := admitTestSpec("c1")
	spec.PauseHeldUpstream = true
	lease, err := a.Admit(spec)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	lease.Release()
}

// EndRoom frees the room early without letting the later Release end a job a
// newer turn has started in the same room.
func TestLeaseEndRoomIsOnlyAppliedOnce(t *testing.T) {
	a := newAdmitTestAdmission(t, 2)
	lease, err := a.Admit(admitTestSpec("c1"))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	lease.EndRoom()
	if a.Broadcaster.IsBusy("c1") {
		t.Fatal("EndRoom did not free the room")
	}
	if a.Drainer.InFlight() != 1 {
		t.Fatal("EndRoom released more than the room")
	}
	if !a.Broadcaster.StartJob("c1", func() {}) {
		t.Fatal("next turn could not claim the freed room")
	}
	lease.Release()
	if !a.Broadcaster.IsBusy("c1") {
		t.Fatal("Release ended the next turn's room")
	}
	a.Broadcaster.EndJob("c1")
}

// A lease's gate re-enters the queue through the same admission rules after a
// parked question, reporting the requeue and applying Describe to failures.
func TestLeaseGateRequeuesThroughTheAdmission(t *testing.T) {
	a := newAdmitTestAdmission(t, 1)
	spec := admitTestSpec("c1")
	var requeues int
	spec.OnTicket = func(_ *Ticket, requeue bool) {
		if requeue {
			requeues++
		}
	}
	spec.Describe = func(err error) error { return errors.New("described") }
	lease, err := a.Admit(spec)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	defer lease.Release()

	lease.Gate().Park()
	if jobs := a.Drainer.Jobs(); jobs[0].Status != JobStatusWaiting {
		t.Fatalf("status after park = %q, want waiting", jobs[0].Status)
	}
	if err := lease.Gate().Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if requeues != 1 {
		t.Fatalf("requeues = %d, want 1", requeues)
	}
	if jobs := a.Drainer.Jobs(); jobs[0].Status != JobStatusRunning {
		t.Fatalf("status after resume = %q, want running", jobs[0].Status)
	}

	lease.Gate().Park()
	a.Pool.Cooldown(admitTestAccount, "m", time.Now().Add(time.Hour))
	if err := lease.Gate().Resume(context.Background()); err == nil || err.Error() != "described" {
		t.Fatalf("Resume err = %v, want the described refusal", err)
	}
}

// The web wording keeps a queue timeout actionable and never a cancel.
func TestWebAdmissionError(t *testing.T) {
	err := webAdmissionError(&QueueTimeoutError{Account: "acc1", Waited: 50 * time.Millisecond})
	if errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), `account "acc1" has no free slot: gave up waiting after 50ms`) {
		t.Fatalf("timeout = %v", err)
	}
	if got := webAdmissionError(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Fatalf("cancel = %v, want it passed through", got)
	}
	cd := &CooldownError{Account: "acc1", Until: time.Now()}
	if got := webAdmissionError(&PoolEntryError{Err: cd}); got != error(cd) { //nolint:errorlint // identity: the pool's own error must come back unwrapped
		t.Fatalf("pool entry = %v, want the pool's own error", got)
	}
}
