package wechat

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// CursorStore persists the get_updates_buf between polls and across restarts.
// Losing it is not fatal but replays or skips messages, so the file-backed
// implementation is the right default for anything long-lived.
type CursorStore interface {
	Load() (string, error)
	Save(cursor string) error
}

// MemoryCursor keeps the cursor in memory only. Suitable for tests and for
// throwaway sessions where replaying the backlog on restart is acceptable.
type MemoryCursor struct {
	mu     sync.Mutex
	cursor string
}

func (m *MemoryCursor) Load() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cursor, nil
}

func (m *MemoryCursor) Save(cursor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursor = cursor
	return nil
}

type sessionExpiredError struct{}

func (sessionExpiredError) Error() string {
	return "wechat: bot session expired, re-pair the account"
}

func (sessionExpiredError) Permanent() bool { return true }

// ErrSessionExpired ends a poll loop whose bot token the gateway no longer
// accepts. Retrying cannot fix it — the account must be re-paired by scanning
// a new QR code — so both the poller and connector supervisor stop immediately.
var ErrSessionExpired error = sessionExpiredError{}

// Poller runs the long-poll receive loop.
//
// Handle is invoked synchronously, in arrival order, so a slow handler stalls
// the loop. That is deliberate at this layer: ordering is what makes an
// in-band command like /new mean "everything after this point", and the
// concurrency policy belongs to whatever bridges this into DayMug.
type Poller struct {
	Client *Client
	Cursor CursorStore
	Handle func(ctx context.Context, msg *Message)
	Logf   func(format string, args ...any)

	// LongPollTimeout bounds one poll. Defaults to DefaultLongPollTimeout.
	LongPollTimeout time.Duration
	// RetryDelay is the pause after a single failed poll.
	RetryDelay time.Duration
	// BackoffDelay replaces RetryDelay once MaxConsecutiveFailures polls in a
	// row have failed, so a gateway outage is not hammered.
	BackoffDelay           time.Duration
	MaxConsecutiveFailures int

	// sleep is injectable so tests do not spend real time in backoff.
	sleep func(ctx context.Context, d time.Duration) error
}

func (p *Poller) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

func (p *Poller) delays() (retry, backoff time.Duration, maxFailures int) {
	retry = p.RetryDelay
	if retry <= 0 {
		retry = 2 * time.Second
	}
	backoff = p.BackoffDelay
	if backoff <= 0 {
		backoff = 30 * time.Second
	}
	maxFailures = p.MaxConsecutiveFailures
	if maxFailures <= 0 {
		maxFailures = 5
	}
	return retry, backoff, maxFailures
}

func (p *Poller) nap(ctx context.Context, d time.Duration) error {
	if p.sleep != nil {
		return p.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Run polls until ctx is cancelled (returns nil) or the session expires
// (returns ErrSessionExpired).
func (p *Poller) Run(ctx context.Context) error {
	if p.Client == nil {
		return errors.New("wechat: poller needs a client")
	}
	cursorStore := p.Cursor
	if cursorStore == nil {
		cursorStore = &MemoryCursor{}
	}
	retryDelay, backoffDelay, maxFailures := p.delays()

	cursor, err := cursorStore.Load()
	if err != nil {
		// A cursor we cannot read is recoverable: starting from scratch
		// replays a bounded backlog, which beats refusing to start.
		p.logf("wechat: cursor unreadable, starting fresh: %v", err)
		cursor = ""
	}
	if cursor == "" {
		p.logf("wechat: no saved cursor, starting fresh")
	}

	failures := 0
	for {
		if ctx.Err() != nil {
			return nil
		}

		resp, err := p.Client.GetUpdates(ctx, cursor, p.LongPollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// A client-side deadline is how an idle long poll ends. It is
			// the common case, not a failure, so it must not feed backoff.
			if errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			var permanent interface{ Permanent() bool }
			if errors.As(err, &permanent) && permanent.Permanent() {
				return err
			}
			failures++
			wait := retryDelay
			if failures >= maxFailures {
				wait = backoffDelay
			}
			p.logf("wechat: getUpdates failed (%d consecutive): %v — retrying in %s", failures, err, wait)
			if napErr := p.nap(ctx, wait); napErr != nil {
				return nil
			}
			continue
		}

		if resp.SessionExpired() {
			return fmt.Errorf("%w (errcode=%d ret=%d %s)", ErrSessionExpired, resp.ErrCode, resp.Ret, resp.ErrMsg)
		}
		if resp.Failed() {
			apiErr := &APIError{Endpoint: epGetUpdates, Ret: resp.Ret, ErrCode: resp.ErrCode, Message: resp.ErrMsg}
			if apiErr.Permanent() {
				return apiErr
			}
			failures++
			wait := retryDelay
			if failures >= maxFailures {
				wait = backoffDelay
			}
			p.logf("wechat: getUpdates rejected ret=%d errcode=%d %s (%d consecutive) — retrying in %s",
				resp.Ret, resp.ErrCode, resp.ErrMsg, failures, wait)
			if napErr := p.nap(ctx, wait); napErr != nil {
				return nil
			}
			continue
		}
		failures = 0

		// Persist before dispatching. A handler that crashes the process
		// should not make the gateway replay the message that crashed it.
		if resp.GetUpdatesBuf != "" && resp.GetUpdatesBuf != cursor {
			cursor = resp.GetUpdatesBuf
			if err := cursorStore.Save(cursor); err != nil {
				p.logf("wechat: cursor not persisted, restart will replay: %v", err)
			}
		}

		for _, msg := range resp.Msgs {
			if msg == nil {
				continue
			}
			p.dispatch(ctx, msg)
		}
	}
}

// dispatch isolates handler panics so one malformed message cannot take the
// receive loop down with it.
func (p *Poller) dispatch(ctx context.Context, msg *Message) {
	defer func() {
		if r := recover(); r != nil {
			p.logf("wechat: handler panicked on message from %s: %v", msg.PeerID(), r)
		}
	}()
	if p.Handle != nil {
		p.Handle(ctx, msg)
	}
}
