package store

import (
	"context"
	"errors"
	"testing"
)

func TestCreateUser_DefaultsSandboxModeToJailed(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "u1", Username: "alice", WorkDir: "/home/alice"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetUser(ctx, "u1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SandboxMode != SandboxModeJailed {
		t.Errorf("default sandbox_mode: got %q want %q", got.SandboxMode, SandboxModeJailed)
	}
	if SandboxUnrestricted(got.SandboxMode) {
		t.Error("a freshly created user must not be unrestricted")
	}
}

func TestSetUserSandboxMode_RoundTrips(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "u1", Username: "alice", WorkDir: "/home/alice"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetUserSandboxMode(ctx, "u1", SandboxModeUnrestricted); err != nil {
		t.Fatalf("set unrestricted: %v", err)
	}
	got, _ := s.GetUser(ctx, "u1")
	if !SandboxUnrestricted(got.SandboxMode) {
		t.Errorf("expected unrestricted after set, got %q", got.SandboxMode)
	}
}

func TestSetUserSandboxMode_NormalisesUnknownToJailed(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "u1", Username: "alice", WorkDir: "/home/alice"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.SetUserSandboxMode(ctx, "u1", "garbage"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, _ := s.GetUser(ctx, "u1")
	if got.SandboxMode != SandboxModeJailed {
		t.Errorf("unknown mode should normalise to jailed, got %q", got.SandboxMode)
	}
}

func TestSetUserSandboxMode_MissingUser(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if err := s.SetUserSandboxMode(context.Background(), "nope", SandboxModeUnrestricted); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
