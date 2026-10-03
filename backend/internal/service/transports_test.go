package service

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// Every model now comes from the admin registry; a compiled-in id would
// reappear in every picker the moment an account had no row.
func TestShippedProviderModelsAreEmpty(t *testing.T) {
	for provider, models := range shippedProviderModels {
		if len(models) != 0 {
			t.Errorf("%s ships built-in models %v", provider, models)
		}
	}
	for _, provider := range config.SupportedCLITypes {
		if _, ok := shippedProviderModels[provider]; !ok {
			t.Errorf("%s missing from ProviderModels, so IsValidProvider rejects it", provider)
		}
	}
}

func TestNormalizeTransports(t *testing.T) {
	cases := []struct {
		name    string
		in      map[string]string
		want    map[string]string
		wantErr bool
	}{
		{"defaults are dropped", map[string]string{"claude": "agent-sdk", "codex": "app-server"}, map[string]string{}, false},
		{"cli is kept", map[string]string{"claude": "cli", "codex": " cli "}, map[string]string{"claude": "cli", "codex": "cli"}, false},
		{"blank means default", map[string]string{"claude": ""}, map[string]string{}, false},
		{"wrong family", map[string]string{"claude": "app-server"}, nil, true},
		{"compatible type is fixed", map[string]string{"openai-compatible": "cli"}, nil, true},
		{"unknown type", map[string]string{"gemini": "cli"}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeTransports(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestSaveTransportsMergesPersistsAndReloads(t *testing.T) {
	t.Cleanup(func() { SetTransports(nil) })
	SetTransports(nil)
	ctx := context.Background()
	ms := storetest.New()

	if err := SaveTransports(ctx, ms, map[string]string{"claude": "cli"}); err != nil {
		t.Fatalf("save claude: %v", err)
	}
	// A save naming only codex must not reset claude.
	if err := SaveTransports(ctx, ms, map[string]string{"codex": "cli"}); err != nil {
		t.Fatalf("save codex: %v", err)
	}
	SetTransports(nil)
	if err := LoadTransports(ctx, ms); err != nil {
		t.Fatalf("load: %v", err)
	}
	if TransportFor("claude") != TransportCLI || TransportFor("codex") != TransportCLI {
		t.Fatalf("after reload claude=%q codex=%q, want both cli", TransportFor("claude"), TransportFor("codex"))
	}
	if err := SaveTransports(ctx, ms, map[string]string{"claude": "agent-sdk", "codex": "app-server"}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := ms.AppSettings[TransportsKey]; got != "" {
		t.Fatalf("stored = %q, want the row cleared once everything is default", got)
	}
}

// Two admins saving different provider types at once must both land; before
// the save was serialised each merged onto the same stale snapshot and the
// later write dropped the other's change without an error.
func TestSaveTransportsConcurrentSavesKeepBoth(t *testing.T) {
	t.Cleanup(func() { SetTransports(nil) })
	ctx := context.Background()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "transports.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := s.Init(); err != nil {
		t.Fatalf("init store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	for i := 0; i < 50; i++ {
		SetTransports(nil)
		if err := s.SetAppSetting(ctx, TransportsKey, ""); err != nil {
			t.Fatalf("reset: %v", err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, provider := range []string{"claude", "codex"} {
			wg.Add(1)
			go func(provider string) {
				defer wg.Done()
				<-start
				if err := SaveTransports(ctx, s, map[string]string{provider: TransportCLI}); err != nil {
					t.Errorf("save %s: %v", provider, err)
				}
			}(provider)
		}
		close(start)
		wg.Wait()

		SetTransports(nil)
		if err := LoadTransports(ctx, s); err != nil {
			t.Fatalf("load: %v", err)
		}
		if TransportFor("claude") != TransportCLI || TransportFor("codex") != TransportCLI {
			t.Fatalf("iteration %d: claude=%q codex=%q, want both cli", i, TransportFor("claude"), TransportFor("codex"))
		}
	}
}
