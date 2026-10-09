package store

import (
	"context"
	"testing"
	"time"
)

// The reap used to compare expires_at against SQLite's datetime('now'), which
// formats with a space while the driver writes time.Time with a 'T'. String
// order disagreed with chronological order and nothing was ever deleted.
func TestDeleteExpiredSessions_ReapsOnlyExpired(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateSession(ctx, Session{Token: "stale", UserID: "u1", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatalf("create stale session: %v", err)
	}
	if err := s.CreateSession(ctx, Session{Token: "live", UserID: "u1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("create live session: %v", err)
	}

	n, err := s.deleteExpiredSessions(ctx)
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if n != 1 {
		t.Fatalf("deleted %d sessions, want 1", n)
	}
	if _, err := s.GetSession(ctx, "live"); err != nil {
		t.Fatalf("live session should survive: %v", err)
	}
}
