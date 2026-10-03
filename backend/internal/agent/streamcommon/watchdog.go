package streamcommon

import (
	"fmt"
	"time"
)

// WatchdogConfig is the liveness contract every adapter's read loop shares.
// The failure it catches is transport-independent: a turn that goes quiet
// forever otherwise holds its account-pool slot and shows as "running" in the
// UI until a human notices, whether the silence is on a pipe (StreamLines) or
// on a JSON-RPC subscription (codexapp).
type WatchdogConfig struct {
	// Source names the silent party in the error ("claude CLI", "codex CAS")
	// so the user sees which child went quiet.
	Source string
	// StallTimeout is the max silence before the activity probe is consulted.
	// Zero means DefaultStallTimeout, so an unset config field can never switch
	// the watchdog off.
	StallTimeout time.Duration
	// MaxSilentTimeout caps one silent stretch even when ActivityProbe keeps
	// reporting progress. Zero means no absolute cap.
	MaxSilentTimeout time.Duration
	// ActivityProbe is called when StallTimeout expires. If it reports
	// activity, the stall timer is re-armed instead of failing the run.
	ActivityProbe func() (bool, string)
	// StallExempt suppresses both budgets while it reports true, and also
	// restarts the silence clock — see LineStream.StallExempt.
	StallExempt func() bool
	// Now is the clock used to measure silence; nil means time.Now. A seam so
	// adapters with their own test clock can drive MaxSilentTimeout.
	Now func() time.Time
}

// Watchdog tracks one stream's silence. The caller owns the select loop: it
// calls Activity whenever output arrives and Expired whenever C fires. It is
// not safe for concurrent use — it lives inside that single loop.
type Watchdog struct {
	cfg        WatchdogConfig
	timer      *time.Timer
	lastOutput time.Time
}

// NewWatchdog arms the stall timer. Stop it when the loop exits.
func NewWatchdog(cfg WatchdogConfig) *Watchdog {
	if cfg.StallTimeout <= 0 {
		cfg.StallTimeout = DefaultStallTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Watchdog{cfg: cfg, timer: time.NewTimer(cfg.StallTimeout), lastOutput: cfg.Now()}
}

// C fires when the stream has been silent for StallTimeout.
func (w *Watchdog) C() <-chan time.Time { return w.timer.C }

// Activity records that the stream produced output.
func (w *Watchdog) Activity() {
	w.lastOutput = w.cfg.Now()
	w.rearm()
}

// Expired decides what a fired timer means. It returns nil after re-arming
// when the silence is excused (exempt, or the probe sees progress), and the
// stall error when the caller should give up on the stream.
func (w *Watchdog) Expired() error {
	// Checked ahead of both budgets: an exempt stream is not silent because
	// something hung, it is silent because nobody asked it anything yet.
	if w.cfg.StallExempt != nil && w.cfg.StallExempt() {
		w.Activity()
		return nil
	}
	silentFor := w.cfg.Now().Sub(w.lastOutput)
	if w.cfg.MaxSilentTimeout > 0 && silentFor >= w.cfg.MaxSilentTimeout {
		return fmt.Errorf("upstream unreachable: no output from %s for %s (maximum silent duration %s exceeded)", w.cfg.Source, silentFor.Round(time.Second), w.cfg.MaxSilentTimeout)
	}
	if w.cfg.ActivityProbe != nil {
		if active, _ := w.cfg.ActivityProbe(); active {
			w.rearm()
			return nil
		}
	}
	return fmt.Errorf("upstream unreachable: no output from %s for %s (the model endpoint may be down or rate-limited)", w.cfg.Source, w.cfg.StallTimeout)
}

// Stop releases the timer.
func (w *Watchdog) Stop() { w.timer.Stop() }

func (w *Watchdog) rearm() {
	if !w.timer.Stop() {
		select {
		case <-w.timer.C:
		default:
		}
	}
	w.timer.Reset(w.cfg.StallTimeout)
}
