package store

import (
	"context"
	"errors"
	"testing"
)

// ownedAgentIDs returns the ids of ownerID's agents in ListUsers order.
func ownedAgentIDs(t *testing.T, s *SQLiteStore, ownerID string) []string {
	t.Helper()
	users, err := s.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	var ids []string
	for _, u := range users {
		if u.Username == "" && u.OwnerID == ownerID {
			ids = append(ids, u.ID)
		}
	}
	return ids
}

func TestReorderAgents(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	mustCreate := func(u User) {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatalf("create %s: %v", u.ID, err)
		}
	}
	mustCreate(User{ID: "h1", Name: "Human", Username: "human", Email: "h@x"})
	mustCreate(User{ID: "a1", Name: "A1", OwnerID: "h1"})
	mustCreate(User{ID: "a2", Name: "A2", OwnerID: "h1"})
	mustCreate(User{ID: "a3", Name: "A3", OwnerID: "h1"})

	if err := s.ReorderAgents(ctx, "h1", []string{"a3", "a1", "a2"}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	got := ownedAgentIDs(t, s, "h1")
	want := []string{"a3", "a1", "a2"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d = %s, want %s (got %v)", i, got[i], want[i], got)
		}
	}
}

// Ids the caller doesn't own must not be reordered — ReorderAgents scopes its
// updates to (owner_id, username=”) so a cross-owner id is a silent no-op.
func TestReorderAgents_IgnoresForeignIDs(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	for _, u := range []User{
		{ID: "h1", Name: "H1", Username: "h1", Email: "h1@x"},
		{ID: "h2", Name: "H2", Username: "h2", Email: "h2@x"},
		{ID: "mine", Name: "Mine", OwnerID: "h1"},
		{ID: "theirs", Name: "Theirs", OwnerID: "h2"},
	} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatalf("create %s: %v", u.ID, err)
		}
	}
	if err := s.ReorderAgents(ctx, "h1", []string{"theirs", "mine"}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	other, err := s.GetUser(ctx, "theirs")
	if err != nil {
		t.Fatalf("get theirs: %v", err)
	}
	if other.SortOrder != 0 {
		t.Errorf("foreign agent sort_order changed to %d, want 0", other.SortOrder)
	}
}

// The human owner can be reordered among its own agents — ReorderAgents stamps
// the caller's own row (id == ownerID) too, so it can be interleaved.
func TestReorderAgents_IncludesOwner(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	for _, u := range []User{
		{ID: "h1", Name: "Human", Username: "human", Email: "h@x"},
		{ID: "a1", Name: "A1", OwnerID: "h1"},
		{ID: "a2", Name: "A2", OwnerID: "h1"},
	} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatalf("create %s: %v", u.ID, err)
		}
	}
	// Place the human between the two agents.
	if err := s.ReorderAgents(ctx, "h1", []string{"a1", "h1", "a2"}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	var got []string
	for _, u := range users {
		if u.Owner() == "h1" {
			got = append(got, u.ID)
		}
	}
	want := []string{"a1", "h1", "a2"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d = %s, want %s (got %v)", i, got[i], want[i], got)
		}
	}
}

func TestArchiveUnarchiveAgent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	for _, u := range []User{
		{ID: "h1", Name: "H1", Username: "h1", Email: "h1@x"},
		{ID: "a1", Name: "A1", OwnerID: "h1"},
		{ID: "a2", Name: "A2", OwnerID: "h1"},
	} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatalf("create %s: %v", u.ID, err)
		}
	}

	if err := s.ArchiveUser(ctx, "a1"); err != nil {
		t.Fatalf("archive a1: %v", err)
	}
	// Idempotent: archiving an already-archived row is ErrNotFound.
	if err := s.ArchiveUser(ctx, "a1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("re-archive: want ErrNotFound, got %v", err)
	}

	a1, err := s.GetUser(ctx, "a1")
	if err != nil {
		t.Fatalf("get a1: %v", err)
	}
	if !a1.Archived {
		t.Error("a1 should be archived")
	}

	archived, err := s.ListArchivedAgents(ctx, "h1")
	if err != nil {
		t.Fatalf("list archived: %v", err)
	}
	if len(archived) != 1 || archived[0].ID != "a1" {
		t.Fatalf("expected [a1] archived, got %+v", archived)
	}

	if err := s.UnarchiveUser(ctx, "a1"); err != nil {
		t.Fatalf("unarchive a1: %v", err)
	}
	if err := s.UnarchiveUser(ctx, "a1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("re-unarchive: want ErrNotFound, got %v", err)
	}
	a1, err = s.GetUser(ctx, "a1")
	if err != nil {
		t.Fatalf("get a1 after restore: %v", err)
	}
	if a1.Archived {
		t.Error("a1 should be active after restore")
	}
	archived, err = s.ListArchivedAgents(ctx, "h1")
	if err != nil {
		t.Fatalf("list archived after restore: %v", err)
	}
	if len(archived) != 0 {
		t.Fatalf("expected no archived agents, got %+v", archived)
	}
}
