package store

import (
	"context"
	"fmt"
	"log"
	"time"
)

const (
	maintenanceFirstDelay = 5 * time.Minute
	maintenanceInterval   = 6 * time.Hour
)

func (s *SQLiteStore) startMaintenance() {
	s.maintenanceOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.maintenanceCancel = cancel
		s.maintenanceDone = make(chan struct{})
		go s.maintenanceLoop(ctx)
	})
}

func (s *SQLiteStore) maintenanceLoop(ctx context.Context) {
	defer close(s.maintenanceDone)
	timer := time.NewTimer(maintenanceFirstDelay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if _, _, err := s.RunMaintenance(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[store] maintenance sweep: %v", err)
		}
		timer.Reset(maintenanceInterval)
	}
}

func (s *SQLiteStore) stopMaintenance() {
	if s.maintenanceCancel == nil {
		return
	}
	s.maintenanceCancel()
	<-s.maintenanceDone
}

// SetPurgeRetention sets how long soft-deleted users, agents, and
// conversations survive before RunMaintenance erases them. Zero or negative
// disables the automatic purge.
func (s *SQLiteStore) SetPurgeRetention(d time.Duration) {
	s.purgeAfter.Store(int64(d))
}

// SetInactiveRetention sets how long a conversation may go without activity
// before RunMaintenance deletes it, exactly as if its owner had. Zero or
// negative disables the sweep.
func (s *SQLiteStore) SetInactiveRetention(d time.Duration) {
	s.inactiveAfter.Store(int64(d))
}

// ConversationRef identifies a deleted conversation and the agent it belonged
// to — enough to locate the agent-scoped files it referenced.
type ConversationRef struct {
	ID     string
	UserID string
}

// SetInactiveDeletedHook registers fn to run after each maintenance sweep that
// soft-deleted inactive conversations. The store owns no files, so this is how
// the uploads those conversations referenced get removed just as they are when
// the owner deletes a conversation by hand.
func (s *SQLiteStore) SetInactiveDeletedHook(fn func(context.Context, []ConversationRef)) {
	if fn == nil {
		s.onInactiveDeleted.Store(nil)
		return
	}
	s.onInactiveDeleted.Store(&fn)
}

// deleteInactiveConversations soft-deletes every live conversation not
// updated since cutoff. Pinned ones are skipped: pinning is the user's explicit
// request to keep a conversation around. Open tabs are not notified; the row
// disappears from their sidebar on the next reload, which is acceptable for
// conversations nobody has touched in the whole retention window.
func (s *SQLiteStore) deleteInactiveConversations(ctx context.Context, cutoff time.Time) ([]ConversationRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		UPDATE conversations
		SET deleted_at = ?
		WHERE deleted_at IS NULL AND pinned = 0 AND updated_at < ?
		RETURNING id, user_id`, time.Now(), cutoff)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var refs []ConversationRef
	for rows.Next() {
		var ref ConversationRef
		if err := rows.Scan(&ref.ID, &ref.UserID); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

// RunMaintenance removes expired login sessions and, when the matching
// retention is configured, deletes inactive conversations and erases
// soft-deleted data past its window. It deliberately does not VACUUM;
// rewriting the database remains an explicit administrator action.
func (s *SQLiteStore) RunMaintenance(ctx context.Context) (int64, int64, error) {
	sessions, err := s.deleteExpiredSessions(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	if inactive := time.Duration(s.inactiveAfter.Load()); inactive > 0 {
		refs, err := s.deleteInactiveConversations(ctx, time.Now().Add(-inactive))
		if err != nil {
			return sessions, 0, fmt.Errorf("delete inactive conversations: %w", err)
		}
		if len(refs) > 0 {
			log.Printf("[store] maintenance deleted %d conversation(s) inactive for over %s", len(refs), inactive)
			if fn := s.onInactiveDeleted.Load(); fn != nil {
				(*fn)(ctx, refs)
			}
		}
	}
	retention := time.Duration(s.purgeAfter.Load())
	if retention <= 0 {
		if sessions > 0 {
			log.Printf("[store] maintenance removed %d expired session(s)", sessions)
		}
		return sessions, 0, nil
	}
	cutoff := time.Now().Add(-retention)
	users, agents, conversations, err := s.purgeSoftDeletedUsers(ctx, cutoff)
	if err != nil {
		return sessions, 0, err
	}
	otherConversations, err := s.purgeSoftDeletedConversations(ctx, cutoff)
	if err != nil {
		return sessions, conversations, err
	}
	conversations += otherConversations
	if sessions > 0 || users > 0 || agents > 0 || conversations > 0 {
		log.Printf("[store] maintenance removed %d expired session(s), %d aged user(s), %d aged agent(s), and %d aged conversation(s)", sessions, users, agents, conversations)
	}
	return sessions, conversations, nil
}
