package storetest

import (
	"context"
	"sort"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// marketplaceCreatorName mirrors the SQL COALESCE(name, username, created_by):
// a publisher who has since been deleted still shows up by id. The join is on
// the users table, which holds humans only (agents live in their own table).
func (m *Fake) marketplaceCreatorName(userID string) string {
	for _, u := range m.Users {
		if u.ID != userID || u.Username == "" {
			continue
		}
		if u.Name != "" {
			return u.Name
		}
		return u.Username
	}
	return userID
}

func (m *Fake) marketplaceView(app store.MarketplaceApp) store.MarketplaceApp {
	app.CreatedByName = m.marketplaceCreatorName(app.CreatedBy)
	return app
}

func (m *Fake) CreateMarketplaceApp(_ context.Context, app store.MarketplaceApp) (store.MarketplaceApp, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	if app.ID == "" {
		app.ID = uuid.NewString()
	}
	app.CreatedByName = ""
	app.CreatedAt = fakeNow()
	m.MarketplaceApps = append(m.MarketplaceApps, app)
	return m.marketplaceView(app), nil
}

// UpdateMarketplaceApp rewrites the editable fields only: CreatedBy keeps the
// original publisher on record, as in SQLite.
func (m *Fake) UpdateMarketplaceApp(_ context.Context, app store.MarketplaceApp) (store.MarketplaceApp, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i := range m.MarketplaceApps {
		cur := &m.MarketplaceApps[i]
		if cur.ID != app.ID {
			continue
		}
		cur.Name = app.Name
		cur.Description = app.Description
		cur.URL = app.URL
		cur.IconURL = app.IconURL
		cur.DeployDir = app.DeployDir
		return m.marketplaceView(*cur), nil
	}
	return store.MarketplaceApp{}, store.ErrNotFound
}

func (m *Fake) ListMarketplaceApps(_ context.Context) ([]store.MarketplaceApp, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	apps := make([]store.MarketplaceApp, 0, len(m.MarketplaceApps))
	for _, app := range m.MarketplaceApps {
		apps = append(apps, m.marketplaceView(app))
	}
	sort.SliceStable(apps, func(i, j int) bool {
		if !apps[i].CreatedAt.Equal(apps[j].CreatedAt) {
			return apps[i].CreatedAt.After(apps[j].CreatedAt)
		}
		return apps[i].ID > apps[j].ID
	})
	return apps, nil
}

func (m *Fake) DeleteMarketplaceApp(_ context.Context, id string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i, app := range m.MarketplaceApps {
		if app.ID == id {
			m.MarketplaceApps = append(m.MarketplaceApps[:i], m.MarketplaceApps[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}
