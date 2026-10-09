package service

import (
	"context"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func TestProviderSettingsSaveAndLoad(t *testing.T) {
	ctx := context.Background()
	settings := storetest.New()
	cfg := &config.Config{}
	providers := []config.Provider{{Name: "primary", Type: config.CLITypeClaude, MaxConcurrent: 3}}

	if err := SaveProviderSettings(ctx, settings, cfg, providers); err != nil {
		t.Fatalf("save providers: %v", err)
	}
	if got := cfg.ProviderSnapshot(); len(got) != 1 || got[0].Name != "primary" {
		t.Fatalf("runtime providers = %#v", got)
	}

	reloaded := &config.Config{}
	if err := LoadProviderSettings(ctx, settings, reloaded); err != nil {
		t.Fatalf("load providers: %v", err)
	}
	if got := reloaded.ProviderSnapshot(); len(got) != 1 || got[0].MaxConcurrent != 3 {
		t.Fatalf("reloaded providers = %#v", got)
	}
}

// Providers live only in SQLite: whatever the cache held before the load, a
// missing row or an explicit [] leaves no runnable provider and writes nothing.
func TestProviderSettingsLoadWithoutRowClearsCache(t *testing.T) {
	ctx := context.Background()
	for _, stored := range []string{"", "[]"} {
		settings := storetest.New()
		if stored != "" {
			if err := settings.SetAppSetting(ctx, ProviderSettingsKey, stored); err != nil {
				t.Fatal(err)
			}
		}
		cfg := &config.Config{Providers: []config.Provider{{Name: "stale", Type: config.CLITypeClaude, MaxConcurrent: 1}}}

		if err := LoadProviderSettings(ctx, settings, cfg); err != nil {
			t.Fatalf("load %q: %v", stored, err)
		}
		if got := cfg.ProviderSnapshot(); len(got) != 0 {
			t.Fatalf("load %q left providers %#v", stored, got)
		}
		if got := settings.AppSettings[ProviderSettingsKey]; got != stored {
			t.Fatalf("load %q rewrote the row to %q", stored, got)
		}
	}
}
