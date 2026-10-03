package store

import (
	"context"
	"errors"
	"testing"
)

func TestMarketplaceAppCRUD(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, User{ID: "user-1", Name: "Alice", Username: "alice"}); err != nil {
		t.Fatalf("create publisher: %v", err)
	}

	created, err := s.CreateMarketplaceApp(ctx, MarketplaceApp{
		Name:        "Dashboard",
		Description: "Team metrics",
		URL:         "https://example.com/dashboard",
		IconURL:     "https://example.com/icon.png",
		DeployDir:   "/srv/daymug/dashboard",
		CreatedBy:   "user-1",
	})
	if err != nil {
		t.Fatalf("create marketplace app: %v", err)
	}
	if created.ID == "" || created.CreatedAt.IsZero() {
		t.Fatalf("created app missing generated fields: %+v", created)
	}

	apps, err := s.ListMarketplaceApps(ctx)
	if err != nil {
		t.Fatalf("list marketplace apps: %v", err)
	}
	if len(apps) != 1 || apps[0].Name != "Dashboard" || apps[0].DeployDir != "/srv/daymug/dashboard" ||
		apps[0].CreatedBy != "user-1" || apps[0].CreatedByName != "Alice" {
		t.Fatalf("unexpected apps: %+v", apps)
	}

	updated, err := s.UpdateMarketplaceApp(ctx, MarketplaceApp{
		ID:          created.ID,
		Name:        "Dashboard v2",
		Description: "Team metrics, refreshed",
		URL:         "https://example.com/dashboard-v2",
		IconURL:     "",
		DeployDir:   "",
	})
	if err != nil {
		t.Fatalf("update marketplace app: %v", err)
	}
	if updated.Name != "Dashboard v2" || updated.URL != "https://example.com/dashboard-v2" ||
		updated.IconURL != "" || updated.DeployDir != "" || updated.CreatedBy != "user-1" {
		t.Fatalf("unexpected updated app: %+v", updated)
	}
	if _, err := s.UpdateMarketplaceApp(ctx, MarketplaceApp{ID: "missing", Name: "x", Description: "y", URL: "z"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing app error = %v, want ErrNotFound", err)
	}

	if err := s.DeleteMarketplaceApp(ctx, created.ID); err != nil {
		t.Fatalf("delete marketplace app: %v", err)
	}
	if err := s.DeleteMarketplaceApp(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete error = %v, want ErrNotFound", err)
	}
}
