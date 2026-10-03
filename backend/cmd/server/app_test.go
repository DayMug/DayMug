package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent/pricing"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// recordingSettings is an in-memory app_settings table that remembers the
// order rows were read in.
type recordingSettings struct {
	rows  map[string]string
	fail  map[string]error
	reads []string
}

func (s *recordingSettings) GetAppSetting(_ context.Context, key string) (string, error) {
	s.reads = append(s.reads, key)
	if err := s.fail[key]; err != nil {
		return "", err
	}
	return s.rows[key], nil
}

func (s *recordingSettings) SetAppSetting(_ context.Context, key, value string) error {
	if s.rows == nil {
		s.rows = map[string]string{}
	}
	s.rows[key] = value
	return nil
}

// resetSettingCaches undoes what loadAppSettings writes into the process-wide
// caches, so one test's rows don't leak into the next.
func resetSettingCaches(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		service.SetTransports(nil)
		service.SetModelOverrides(nil)
		pricing.ResetForTest()
	})
}

func TestLoadAppSettingsReadsProvidersAndTransportsFirst(t *testing.T) {
	resetSettingCaches(t)
	st := &recordingSettings{}
	if _, err := loadAppSettings(st, &config.Config{}); err != nil {
		t.Fatalf("loadAppSettings: %v", err)
	}
	want := []string{service.ProviderSettingsKey, service.TransportsKey}
	for _, provider := range pricing.ProvidersWithDefaults() {
		want = append(want, pricing.SettingKey(provider))
	}
	want = append(want, service.ModelOverridesKey)
	if !reflect.DeepEqual(st.reads, want) {
		t.Fatalf("app_settings read order:\n got %v\nwant %v", st.reads, want)
	}
}

// The prerequisite probe is handed the transport lookup by loadAppSettings, so
// it must already reflect what an admin saved.
func TestLoadAppSettingsTransportLookupSeesSavedTransports(t *testing.T) {
	resetSettingCaches(t)
	st := &recordingSettings{rows: map[string]string{
		service.TransportsKey: `{"claude":"cli"}`,
	}}
	settings, err := loadAppSettings(st, &config.Config{})
	if err != nil {
		t.Fatalf("loadAppSettings: %v", err)
	}
	if got := settings.transportFor(config.CLITypeClaude); got != service.TransportCLI {
		t.Fatalf("transportFor(claude) = %q, want %q", got, service.TransportCLI)
	}
}

func TestLoadAppSettingsOnlyProvidersAreFatal(t *testing.T) {
	resetSettingCaches(t)
	boom := errors.New("boom")

	st := &recordingSettings{fail: map[string]error{service.ProviderSettingsKey: boom}}
	if _, err := loadAppSettings(st, &config.Config{}); err == nil || !strings.HasPrefix(err.Error(), "load provider settings: ") || !errors.Is(err, boom) {
		t.Fatalf("provider read failure: err = %v, want a wrapped fatal error", err)
	}
	if len(st.reads) != 1 {
		t.Fatalf("kept loading after a fatal provider error: %v", st.reads)
	}

	st = &recordingSettings{fail: map[string]error{
		service.TransportsKey:     boom,
		service.ModelOverridesKey: boom,
	}}
	for _, provider := range pricing.ProvidersWithDefaults() {
		st.fail[pricing.SettingKey(provider)] = boom
	}
	if _, err := loadAppSettings(st, &config.Config{}); err != nil {
		t.Fatalf("transport/pricing/model failures must degrade, got %v", err)
	}
}

func testAppConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{Users: config.UsersConfig{DefaultHomeRoot: filepath.Join(t.TempDir(), "users")}}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config invalid: %v", err)
	}
	return cfg
}

func TestBuildAppAssemblesAServerAndReleasesIt(t *testing.T) {
	resetSettingCaches(t)
	stubServeRuntimeProbes(t)
	cfg := testAppConfig(t)
	dbPath := filepath.Join(t.TempDir(), "data", "database.db")

	app, err := buildApp(cfg, appOptions{DBPath: dbPath, Version: "test"})
	if err != nil {
		t.Fatalf("buildApp: %v", err)
	}
	defer app.Close()
	if app.Server == nil || app.Server.Handler == nil || app.Drainer == nil {
		t.Fatalf("incomplete app: %+v", app)
	}
	if app.Addr != defaultListenAddr || app.Server.Addr != defaultListenAddr {
		t.Fatalf("addr = %q / %q, want the %s fallback for a blank server.addr", app.Addr, app.Server.Addr, defaultListenAddr)
	}
	if _, err := os.Stat(cfg.Users.DefaultHomeRoot); err != nil {
		t.Fatalf("home root not created: %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database not created: %v", err)
	}
}

func TestBuildAppReturnsStartupErrors(t *testing.T) {
	resetSettingCaches(t)
	stubServeRuntimeProbes(t)

	t.Run("data dir", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := buildApp(testAppConfig(t), appOptions{DBPath: filepath.Join(blocker, "data", "database.db")})
		if err == nil || !strings.HasPrefix(err.Error(), "create data dir: ") {
			t.Fatalf("err = %v, want a create data dir error", err)
		}
	})

	t.Run("runtime prerequisite", func(t *testing.T) {
		serveProbeCodexApp = func(context.Context) error { return errors.New("codex missing") }
		// Providers come only from the database, so seed one there.
		dbPath := filepath.Join(t.TempDir(), "database.db")
		db, err := store.NewSQLiteStore(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Init(); err != nil {
			t.Fatal(err)
		}
		if err := service.SaveProviderSettings(context.Background(), db, &config.Config{},
			[]config.Provider{{Name: "c", Type: config.CLITypeCodex, ConfigDir: t.TempDir(), MaxConcurrent: 1}}); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = buildApp(testAppConfig(t), appOptions{DBPath: dbPath})
		if err == nil || err.Error() != "agent runtime prerequisite: codex missing" {
			t.Fatalf("err = %v, want the prerequisite error", err)
		}
	})
}
