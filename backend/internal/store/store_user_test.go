package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// The users fallback branch omitted default_model, so PUT /api/users/:id on a
// human answered 200 while discarding the submitted model.
func TestUpdateUserPersistsDefaultModelForHumanRow(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "h1", Name: "Alice", Username: "alice"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := s.UpdateUser(ctx, User{ID: "h1", Name: "Alice", DefaultModel: "claude-sonnet-4-5"}); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := s.GetUser(ctx, "h1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DefaultModel != "claude-sonnet-4-5" {
		t.Errorf("default_model = %q, want claude-sonnet-4-5", got.DefaultModel)
	}
}

// Concurrent first-run submissions used to all pass a separate "any users?"
// check and each become admin. With the check inside the insert transaction,
// exactly one wins on a real file database and the rest see ErrSetupClosed.
func TestCreateFirstAdminConcurrentSingleWinner(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "first-admin.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	const n = 10
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			id := fmt.Sprintf("u%d", i)
			errs[i] = s.CreateFirstAdmin(ctx,
				User{ID: id, Name: id, Username: id, Email: id + "@example.com", IsAdmin: true, WorkDir: "/h/" + id},
				User{ID: "a" + id, Name: id, OwnerID: id, WorkDir: "/h/" + id})
		}(i)
	}
	close(start)
	wg.Wait()

	wins := 0
	for i, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrSetupClosed):
		default:
			t.Errorf("submission %d: unexpected error %v", i, err)
		}
	}
	if wins != 1 {
		t.Fatalf("winners = %d, want exactly 1", wins)
	}
	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	humans, agents := 0, 0
	for _, u := range users {
		if u.Username != "" {
			humans++
		} else {
			agents++
		}
	}
	if humans != 1 || agents != 1 {
		t.Errorf("humans=%d agents=%d, want 1 and 1", humans, agents)
	}
}
