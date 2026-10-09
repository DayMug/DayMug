package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// MarketplaceApp is one globally shared link in the application marketplace.
// CreatedBy is retained for auditability only; deletion is deliberately not
// owner-scoped because the marketplace is community-managed.
type MarketplaceApp struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	URL           string    `json:"url"`
	IconURL       string    `json:"icon_url"`
	DeployDir     string    `json:"deploy_dir"`
	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name"`
	CreatedAt     time.Time `json:"created_at"`
}

func (s *SQLiteStore) CreateMarketplaceApp(ctx context.Context, app MarketplaceApp) (MarketplaceApp, error) {
	if app.ID == "" {
		app.ID = uuid.NewString()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO marketplace_apps (id, name, description, url, icon_url, deploy_dir, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		app.ID, app.Name, app.Description, app.URL, app.IconURL, app.DeployDir, app.CreatedBy,
	)
	if err != nil {
		return MarketplaceApp{}, err
	}
	return s.getMarketplaceApp(ctx, app.ID)
}

// UpdateMarketplaceApp rewrites the editable fields of an existing entry.
// CreatedBy is left untouched so the original publisher stays on record even
// after someone else edits the listing.
func (s *SQLiteStore) UpdateMarketplaceApp(ctx context.Context, app MarketplaceApp) (MarketplaceApp, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE marketplace_apps
		SET name = ?, description = ?, url = ?, icon_url = ?, deploy_dir = ?
		WHERE id = ?`,
		app.Name, app.Description, app.URL, app.IconURL, app.DeployDir, app.ID,
	)
	if err != nil {
		return MarketplaceApp{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return MarketplaceApp{}, err
	}
	if n == 0 {
		return MarketplaceApp{}, ErrNotFound
	}
	return s.getMarketplaceApp(ctx, app.ID)
}

func (s *SQLiteStore) getMarketplaceApp(ctx context.Context, id string) (MarketplaceApp, error) {
	var app MarketplaceApp
	err := s.db.QueryRowContext(ctx, `
		SELECT m.id, m.name, m.description, m.url, m.icon_url, m.deploy_dir, m.created_by,
		       COALESCE(NULLIF(u.name, ''), NULLIF(u.username, ''), m.created_by), m.created_at
		FROM marketplace_apps m
		LEFT JOIN users u ON u.id = m.created_by AND u.deleted_at IS NULL
		WHERE m.id = ?`, id,
	).Scan(&app.ID, &app.Name, &app.Description, &app.URL, &app.IconURL, &app.DeployDir,
		&app.CreatedBy, &app.CreatedByName, &app.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MarketplaceApp{}, ErrNotFound
	}
	return app, err
}

func (s *SQLiteStore) ListMarketplaceApps(ctx context.Context) ([]MarketplaceApp, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.name, m.description, m.url, m.icon_url, m.deploy_dir, m.created_by,
		       COALESCE(NULLIF(u.name, ''), NULLIF(u.username, ''), m.created_by), m.created_at
		FROM marketplace_apps m
		LEFT JOIN users u ON u.id = m.created_by AND u.deleted_at IS NULL
		ORDER BY m.created_at DESC, m.id DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	apps := make([]MarketplaceApp, 0)
	for rows.Next() {
		var app MarketplaceApp
		if err := rows.Scan(&app.ID, &app.Name, &app.Description, &app.URL, &app.IconURL, &app.DeployDir,
			&app.CreatedBy, &app.CreatedByName, &app.CreatedAt); err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

func (s *SQLiteStore) DeleteMarketplaceApp(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM marketplace_apps WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
