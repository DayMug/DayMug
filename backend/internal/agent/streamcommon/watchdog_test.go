package streamcommon

import (
	"strings"
	"testing"
	"time"
)

// fakeClock lets a test move the silence clock without sleeping for it.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func waitFired(t *testing.T, w *Watchdog) {
	t.Helper()
	select {
	case <-w.C():
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog timer never fired")
	}
}

func TestWatchdogFailsWhenTheProbeSeesNoActivity(t *testing.T) {
	w := NewWatchdog(WatchdogConfig{
		Source: "codex CAS", StallTimeout: 5 * time.Millisecond,
		ActivityProbe: func() (bool, string) { return false, "" },
	})
	defer w.Stop()
	waitFired(t, w)
	err := w.Expired()
	if err == nil || !strings.Contains(err.Error(), "no output from codex CAS for 5ms (the model endpoint may be down") {
		t.Fatalf("Expired() = %v, want the stall error naming the source and budget", err)
	}
}

func TestWatchdogReArmsWhileTheProbeSeesActivity(t *testing.T) {
	probes := 0
	w := NewWatchdog(WatchdogConfig{
		Source: "claude CLI", StallTimeout: 5 * time.Millisecond,
		ActivityProbe: func() (bool, string) { probes++; return probes < 3, "" },
	})
	defer w.Stop()
	for range 2 {
		waitFired(t, w)
		if err := w.Expired(); err != nil {
			t.Fatalf("Expired() = %v while the probe reports progress", err)
		}
	}
	waitFired(t, w)
	if err := w.Expired(); err == nil {
		t.Fatal("Expired() = nil once the probe stopped reporting progress")
	}
}

func TestWatchdogMaxSilentOverridesAnActiveProbe(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	w := NewWatchdog(WatchdogConfig{
		Source: "codex CAS", StallTimeout: time.Hour, MaxSilentTimeout: time.Minute,
		ActivityProbe: func() (bool, string) { return true, "cpu" },
		Now:           clock.Now,
	})
	defer w.Stop()

	clock.advance(30 * time.Second)
	if err := w.Expired(); err != nil {
		t.Fatalf("Expired() = %v before the absolute cap", err)
	}
	clock.advance(31 * time.Second)
	err := w.Expired()
	if err == nil || !strings.Contains(err.Error(), "maximum silent duration 1m0s exceeded") {
		t.Fatalf("Expired() = %v, want the absolute-cap error", err)
	}
}

func TestWatchdogActivityRestartsTheSilenceClock(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	w := NewWatchdog(WatchdogConfig{
		StallTimeout: time.Hour, MaxSilentTimeout: time.Minute,
		ActivityProbe: func() (bool, string) { return true, "" },
		Now:           clock.Now,
	})
	defer w.Stop()

	clock.advance(50 * time.Second)
	w.Activity()
	clock.advance(50 * time.Second)
	if err := w.Expired(); err != nil {
		t.Fatalf("Expired() = %v, but output arrived 50s ago", err)
	}
}

func TestWatchdogExemptionSuppressesBothBudgetsAndRestartsTheClock(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	exempt := true
	w := NewWatchdog(WatchdogConfig{
		StallTimeout: time.Hour, MaxSilentTimeout: time.Minute,
		ActivityProbe: func() (bool, string) { return false, "" },
		StallExempt:   func() bool { return exempt },
		Now:           clock.Now,
	})
	defer w.Stop()

	clock.advance(10 * time.Minute)
	if err := w.Expired(); err != nil {
		t.Fatalf("Expired() = %v while exempt", err)
	}
	exempt = false
	clock.advance(30 * time.Second)
	err := w.Expired()
	if err == nil || strings.Contains(err.Error(), "maximum silent duration") {
		t.Fatalf("Expired() = %v, want the plain stall error: the exemption restarted the silence clock", err)
	}
}

func TestWatchdogZeroStallMeansDefault(t *testing.T) {
	w := NewWatchdog(WatchdogConfig{})
	defer w.Stop()
	if w.cfg.StallTimeout != DefaultStallTimeout {
		t.Fatalf("StallTimeout = %v, want DefaultStallTimeout", w.cfg.StallTimeout)
	}
}
