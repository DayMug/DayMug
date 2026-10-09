package service

import (
	"context"
	"net/url"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/store"
)

const (
	marketplaceNameLimit        = 100
	marketplaceDescriptionLimit = 500
	marketplaceURLLimit         = 2048
	marketplaceDeployDirLimit   = 2048
)

type MarketplaceOps struct {
	Store store.MarketplaceStore
}

type MarketplaceAppParams struct {
	Name        string
	Description string
	URL         string
	IconURL     string
	DeployDir   string
	CreatedBy   string
}

func (o *MarketplaceOps) List(ctx context.Context) ([]store.MarketplaceApp, error) {
	apps, err := o.Store.ListMarketplaceApps(ctx)
	if err != nil {
		return nil, Internal(err.Error(), err)
	}
	return apps, nil
}

func (o *MarketplaceOps) Create(ctx context.Context, params MarketplaceAppParams) (store.MarketplaceApp, error) {
	params, err := normalizeMarketplaceParams(params)
	if err != nil {
		return store.MarketplaceApp{}, err
	}

	created, err := o.Store.CreateMarketplaceApp(ctx, store.MarketplaceApp{
		Name:        params.Name,
		Description: params.Description,
		URL:         params.URL,
		IconURL:     params.IconURL,
		DeployDir:   params.DeployDir,
		CreatedBy:   params.CreatedBy,
	})
	if err != nil {
		return store.MarketplaceApp{}, Internal(err.Error(), err)
	}
	return created, nil
}

// Update rewrites an existing listing. Like Delete it is deliberately not
// owner-scoped: the marketplace is community-managed.
func (o *MarketplaceOps) Update(ctx context.Context, id string, params MarketplaceAppParams) (store.MarketplaceApp, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return store.MarketplaceApp{}, BadRequest("missing id")
	}
	params, err := normalizeMarketplaceParams(params)
	if err != nil {
		return store.MarketplaceApp{}, err
	}

	updated, err := o.Store.UpdateMarketplaceApp(ctx, store.MarketplaceApp{
		ID:          id,
		Name:        params.Name,
		Description: params.Description,
		URL:         params.URL,
		IconURL:     params.IconURL,
		DeployDir:   params.DeployDir,
	})
	if err != nil {
		return store.MarketplaceApp{}, StoreError(err, "app not found")
	}
	return updated, nil
}

func normalizeMarketplaceParams(params MarketplaceAppParams) (MarketplaceAppParams, error) {
	params.Name = strings.TrimSpace(params.Name)
	params.Description = strings.TrimSpace(params.Description)
	params.URL = strings.TrimSpace(params.URL)
	params.IconURL = strings.TrimSpace(params.IconURL)
	params.DeployDir = strings.TrimSpace(params.DeployDir)

	if params.Name == "" {
		return params, BadRequest("name is required")
	}
	if len([]rune(params.Name)) > marketplaceNameLimit {
		return params, BadRequest("name is too long")
	}
	if params.Description == "" {
		return params, BadRequest("description is required")
	}
	if len([]rune(params.Description)) > marketplaceDescriptionLimit {
		return params, BadRequest("description is too long")
	}
	if err := validateMarketplaceURL(params.URL, false); err != nil {
		return params, err
	}
	if len(params.URL) > marketplaceURLLimit {
		return params, BadRequest("url is too long")
	}
	if err := validateMarketplaceURL(params.IconURL, true); err != nil {
		return params, err
	}
	if len(params.IconURL) > marketplaceURLLimit {
		return params, BadRequest("icon_url is too long")
	}
	if len([]rune(params.DeployDir)) > marketplaceDeployDirLimit {
		return params, BadRequest("deploy_dir is too long")
	}
	return params, nil
}

func validateMarketplaceURL(value string, optional bool) error {
	if value == "" && optional {
		return nil
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" ||
		(!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) {
		if optional {
			return BadRequest("icon_url must be an http or https URL")
		}
		return BadRequest("url must be an http or https URL")
	}
	return nil
}

func (o *MarketplaceOps) Delete(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return BadRequest("missing id")
	}
	if err := o.Store.DeleteMarketplaceApp(ctx, id); err != nil {
		return StoreError(err, "app not found")
	}
	return nil
}
