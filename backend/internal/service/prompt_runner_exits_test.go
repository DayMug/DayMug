package service

import (
	"errors"
	"testing"
	"time"
)

// Every way ProcessPrompt can stop after it was admitted must hand the room,
// the drainer entry and the account slot back — a missed one leaves the
// conversation "busy" and bounces every later prompt. The admission refusals
// are pinned in prompt_runner_admission_test.go; these cover the exits past
// the admission, where the lease is the only thing releasing the room.
func TestProcessPromptReleasesEverythingOnEveryPostAdmissionExit(t *testing.T) {
	tests := []struct {
		name string
		// arrange runs before ProcessPrompt; during runs while it is blocked.
		arrange func(f *admissionFixture, claimed chan<- struct{})
		during  func(t *testing.T, f *admissionFixture, claimed <-chan struct{})
	}{
		{
			name: "prompt recalled after the slot was granted",
			arrange: func(f *admissionFixture, _ chan<- struct{}) {
				// The row is gone by the time the claim runs.
				f.store.Messages[f.convID][0].QueueStatus = ""
			},
		},
		{
			name: "cancelled while the claim is retrying",
			arrange: func(f *admissionFixture, claimed chan<- struct{}) {
				f.store.ClaimErrFn = func(string) error {
					select {
					case claimed <- struct{}{}:
					default:
					}
					return errors.New("SQLITE_IOERR")
				}
			},
			during: func(t *testing.T, f *admissionFixture, claimed <-chan struct{}) {
				select {
				case <-claimed:
				case <-time.After(2 * time.Second):
					t.Fatal("claim never attempted")
				}
				if !f.runner.Broadcaster.CancelJob(f.convID) {
					t.Fatal("claiming turn was not registered with the room")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := newBlockingBackend()
			f := newAdmissionFixture(t, backend)
			claimed := make(chan struct{}, 1)
			tt.arrange(f, claimed)
			done := make(chan struct{})
			go func() {
				defer close(done)
				f.process()
			}()
			if tt.during != nil {
				tt.during(t, f, claimed)
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("ProcessPrompt never returned")
			}
			select {
			case <-backend.started:
				t.Fatal("the agent ran for a prompt that was never claimed")
			default:
			}
			frames := f.drainFrames(t)
			if len(frames) == 0 || frames[len(frames)-1].Status != "ready" {
				t.Fatalf("frames = %+v, want a final ready status", frames)
			}
			for _, fr := range frames {
				if fr.Type == "prompt_started" {
					t.Errorf("prompt_started broadcast for an unclaimed prompt")
				}
			}
			f.assertAllReleased(t)
		})
	}
}

// A deletion racing the dispatcher is reported before anything is taken.
func TestProcessPromptMissingConversationTakesNothing(t *testing.T) {
	f := newAdmissionFixture(t, newBlockingBackend())
	f.store.Conversations = nil
	f.process()
	frames := f.drainFrames(t)
	if len(frames) != 1 || frames[0].Type != "error" || frames[0].Message != "conversation no longer exists" {
		t.Fatalf("frames = %+v, want one missing-conversation error", frames)
	}
	f.assertAllReleased(t)
	if msgs := f.store.Messages[f.convID]; msgs[0].QueueStatus != "pending" {
		t.Errorf("prompt status = %q, want it left pending", msgs[0].QueueStatus)
	}
}
