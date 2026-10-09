package service

import (
	"context"
	"errors"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/store"
)

type marketplaceMemoryStore struct {
	apps []store.MarketplaceApp
}

func (s *marketplaceMemoryStore) CreateMarketplaceApp(_ context.Context, app store.MarketplaceApp) (store.MarketplaceApp, error) {
	app.ID = "app-1"
	s.apps = append(s.apps, app)
	return app, nil
}

func (s *marketplaceMemoryStore) UpdateMarketplaceApp(_ context.Context, app store.MarketplaceApp) (store.MarketplaceApp, error) {
	for i, existing := range s.apps {
		if existing.ID == app.ID {
			app.CreatedBy = existing.CreatedBy
			s.apps[i] = app
			return app, nil
		}
	}
	return store.MarketplaceApp{}, store.ErrNotFound
}

func (s *marketplaceMemoryStore) ListMarketplaceApps(context.Context) ([]store.MarketplaceApp, error) {
	return append([]store.MarketplaceApp(nil), s.apps...), nil
}

func (s *marketplaceMemoryStore) DeleteMarketplaceApp(_ context.Context, id string) error {
	for i, app := range s.apps {
		if app.ID == id {
			s.apps = append(s.apps[:i], s.apps[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func TestMarketplaceOpsCreateNormalizesAndValidatesURLs(t *testing.T) {
	mem := &marketplaceMemoryStore{}
	ops := &MarketplaceOps{Store: mem}

	created, err := ops.Create(context.Background(), MarketplaceAppParams{
		Name:        "  Docs  ",
		Description: "  Shared documentation  ",
		URL:         "https://example.com/docs",
		IconURL:     "https://example.com/icon.svg",
		DeployDir:   "  /srv/daymug/docs  ",
		CreatedBy:   "user-1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Name != "Docs" || created.Description != "Shared documentation" || created.DeployDir != "/srv/daymug/docs" {
		t.Fatalf("values were not normalized: %+v", created)
	}

	_, err = ops.Create(context.Background(), MarketplaceAppParams{
		Name: "Bad", Description: "Unsafe link", URL: "javascript:alert(1)",
	})
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) || serviceErr.Status != 400 {
		t.Fatalf("unsafe URL error = %v, want service 400", err)
	}
}

func TestMarketplaceOpsUpdateValidatesAndKeepsPublisher(t *testing.T) {
	mem := &marketplaceMemoryStore{}
	ops := &MarketplaceOps{Store: mem}
	created, err := ops.Create(context.Background(), MarketplaceAppParams{
		Name: "Docs", Description: "Shared documentation", URL: "https://example.com/docs",
		CreatedBy: "user-1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updated, err := ops.Update(context.Background(), created.ID, MarketplaceAppParams{
		Name: "  Docs v2  ", Description: "  Updated docs  ", URL: "https://example.com/docs-v2",
		DeployDir: "  /srv/daymug/docs  ",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "Docs v2" || updated.Description != "Updated docs" ||
		updated.DeployDir != "/srv/daymug/docs" || updated.CreatedBy != "user-1" {
		t.Fatalf("unexpected updated app: %+v", updated)
	}

	var serviceErr *ServiceError
	if _, err := ops.Update(context.Background(), created.ID, MarketplaceAppParams{
		Name: "Docs", Description: "Unsafe link", URL: "javascript:alert(1)",
	}); !errors.As(err, &serviceErr) || serviceErr.Status != 400 {
		t.Fatalf("unsafe URL error = %v, want service 400", err)
	}

	if _, err := ops.Update(context.Background(), "missing", MarketplaceAppParams{
		Name: "Docs", Description: "Gone", URL: "https://example.com/gone",
	}); !errors.As(err, &serviceErr) || serviceErr.Status != 404 {
		t.Fatalf("missing app error = %v, want service 404", err)
	}
}
