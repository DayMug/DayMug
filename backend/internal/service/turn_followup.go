package service

import (
	"context"
	"errors"
	"log"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// hasQueuedFollowUp reports whether the user already sent another prompt that
// is waiting for this conversation's current turn to end. The dispatcher runs
// one prompt per conversation at a time, so the finishing turn's own row is
// no longer pending: whatever Peek finds will run next. Such a turn is not
// where the user's task ends, so it earns neither the "done" badge nor a push.
func hasQueuedFollowUp(s store.Store, conversationID string) bool {
	if s == nil || conversationID == "" {
		return false
	}
	_, err := s.PeekNextPendingPrompt(context.Background(), conversationID)
	if err == nil {
		return true
	}
	if !errors.Is(err, store.ErrNotFound) {
		log.Printf("conversation %s: peek follow-up prompt: %v", conversationID, err)
	}
	return false
}
