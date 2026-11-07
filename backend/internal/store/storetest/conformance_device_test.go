package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// A device is found by its token hash, listed and revoked only by its owner,
// and revoking it drops the conversations bound to it back to the server.
func TestDeviceLifecycle(t *testing.T) {
	for _, impl := range conformanceImpls() {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.build(t)
			ctx := context.Background()
			createHumanAndAgent(t, s)
			newQueueConversation(t, s, "c1")

			d, err := s.CreateDevice(ctx, store.Device{ID: "d1", UserID: "h1", Name: "laptop", OS: "macos", TokenHash: "hash1"})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if d.CreatedAt.IsZero() || d.Name != "laptop" {
				t.Fatalf("created = %+v", d)
			}
			if _, err := s.CreateDevice(ctx, store.Device{ID: "d2", UserID: "h1", TokenHash: "hash1"}); err == nil {
				t.Fatal("duplicate token hash accepted")
			}
			got, err := s.GetDeviceByTokenHash(ctx, "hash1")
			if err != nil || got.ID != "d1" || got.UserID != "h1" {
				t.Fatalf("by token = %+v, %v", got, err)
			}
			if _, err := s.GetDeviceByTokenHash(ctx, ""); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("empty token err = %v", err)
			}
			if err := s.TouchDevice(ctx, "d1", time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
				t.Fatalf("touch: %v", err)
			}
			if got, _ := s.GetDevice(ctx, "d1"); got.LastSeenAt.Year() != 2031 {
				t.Fatalf("last seen = %v", got.LastSeenAt)
			}
			if list, _ := s.ListDevices(ctx, "h1"); len(list) != 1 {
				t.Fatalf("list = %+v", list)
			}

			if _, err := s.GetConversationDevice(ctx, "c1"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("unbound err = %v", err)
			}
			if err := s.SetConversationDevice(ctx, store.ConversationDevice{ConversationID: "c1", DeviceID: "missing", WorkDir: "/x"}); err == nil {
				t.Fatal("binding to an unknown device accepted")
			}
			for _, dir := range []string{"/a", "/b"} {
				if err := s.SetConversationDevice(ctx, store.ConversationDevice{ConversationID: "c1", DeviceID: "d1", WorkDir: dir}); err != nil {
					t.Fatalf("bind %s: %v", dir, err)
				}
			}
			if b, err := s.GetConversationDevice(ctx, "c1"); err != nil || b.WorkDir != "/b" || b.DeviceID != "d1" {
				t.Fatalf("binding = %+v, %v", b, err)
			}
			if err := s.ClearConversationDevice(ctx, "c1"); err != nil {
				t.Fatalf("clear: %v", err)
			}
			if _, err := s.GetConversationDevice(ctx, "c1"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("cleared err = %v", err)
			}

			if err := s.SetConversationDevice(ctx, store.ConversationDevice{ConversationID: "c1", DeviceID: "d1", WorkDir: "/a"}); err != nil {
				t.Fatalf("rebind: %v", err)
			}
			if err := s.DeleteDevice(ctx, "someone-else", "d1"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("foreign delete err = %v", err)
			}
			if err := s.DeleteDevice(ctx, "h1", "d1"); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if _, err := s.GetConversationDevice(ctx, "c1"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("binding survived revoke: %v", err)
			}
			if _, err := s.GetDeviceByTokenHash(ctx, "hash1"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("revoked token still resolves: %v", err)
			}
		})
	}
}
