package store

import (
	"context"
	"testing"
)

func TestAppSettingMissingKeyIsEmpty(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	v, err := s.GetAppSetting(context.Background(), "missing")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v != "" {
		t.Errorf("unknown key: want empty string, got %q", v)
	}
}

func TestAppSettingSetAndGet(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SetAppSetting(ctx, "k", "v1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.GetAppSetting(ctx, "k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "v1" {
		t.Errorf("first read: want %q got %q", "v1", got)
	}

	// Upsert path: the second write must replace, not append.
	if err := s.SetAppSetting(ctx, "k", "v2"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, _ = s.GetAppSetting(ctx, "k")
	if got != "v2" {
		t.Errorf("after overwrite: want %q got %q", "v2", got)
	}
}

func TestAppSettingClearWithEmptyString(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.SetAppSetting(ctx, "k", "original")
	if err := s.SetAppSetting(ctx, "k", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ := s.GetAppSetting(ctx, "k")
	if got != "" {
		t.Errorf("after clear: want empty, got %q", got)
	}
}
