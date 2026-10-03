// Package imbottest provides imbot.Responder / responder-resolver fakes shared
// by tests in the packages that drive the IM bridge.
package imbottest

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/imbot"
)

// ReplyRecorder captures what the bridge posts back into the thread.
type ReplyRecorder struct {
	mu    sync.Mutex
	texts []string
}

// Record appends text as if it had been posted into the thread.
func (r *ReplyRecorder) Record(text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	return nil
}
func (r *ReplyRecorder) Start(_ context.Context, text string) error    { return r.Record(text) }
func (r *ReplyRecorder) Update(_ context.Context, text string) error   { return r.Record(text) }
func (r *ReplyRecorder) Complete(_ context.Context, text string) error { return r.Record(text) }
func (r *ReplyRecorder) Post(_ context.Context, text string) error     { return r.Record(text) }

// All returns a snapshot of every text posted so far.
func (r *ReplyRecorder) All() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.texts...)
}

// WaitForReplyContaining blocks until the recorder has seen a reply containing
// want, failing the test after two seconds.
func WaitForReplyContaining(t *testing.T, recorder *ReplyRecorder, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, text := range recorder.All() {
			if strings.Contains(text, want) {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("reply containing %q not received; got %v", want, recorder.All())
}

// RecordingResponderResolver hands out a fixed Responder and remembers the last
// (botID, message) pair it was asked about.
type RecordingResponderResolver struct {
	mu        sync.Mutex
	botID     string
	msg       imbot.Message
	Responder imbot.Responder
}

func (r *RecordingResponderResolver) ResponderFor(botID string, msg imbot.Message) (imbot.Responder, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.botID = botID
	r.msg = msg
	return r.Responder, r.Responder != nil
}

// Snapshot returns the last botID and message passed to ResponderFor.
func (r *RecordingResponderResolver) Snapshot() (string, imbot.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.botID, r.msg
}
