package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
)

func TestRunInit_GeneratesValidConfig(t *testing.T) {
	dir := t.TempDir()

	configPath, homeRoot, dataDir, err := runInit(dir, false)
	if err != nil {
		t.Fatalf("runInit: %v", err)
	}

	if configPath != filepath.Join(dir, "config.yaml") {
		t.Errorf("configPath = %q, want %q", configPath, filepath.Join(dir, "config.yaml"))
	}
	if homeRoot != filepath.Join(dir, "users") {
		t.Errorf("homeRoot = %q, want %q", homeRoot, filepath.Join(dir, "users"))
	}
	if dataDir != filepath.Join(dir, "data") {
		t.Errorf("dataDir = %q, want %q", dataDir, filepath.Join(dir, "data"))
	}

	// data/ and users/ should exist after init.
	for _, p := range []string{dataDir, homeRoot} {
		info, statErr := os.Stat(p)
		if statErr != nil {
			t.Errorf("expected directory %s to exist: %v", p, statErr)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", p)
		}
	}

	// Generated config.yaml should load and validate cleanly.
	cfg, loadErr := config.Load(configPath)
	if loadErr != nil {
		t.Fatalf("config.Load(generated): %v", loadErr)
	}

	if cfg.Users.DefaultHomeRoot != homeRoot {
		t.Errorf("users.default_home_root = %q, want %q", cfg.Users.DefaultHomeRoot, homeRoot)
	}
	if got := cfg.ProviderSnapshot(); len(got) != 0 {
		t.Errorf("generated config should leave providers to the admin UI, got %#v", got)
	}
}

func TestRunInit_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()

	if _, _, _, err := runInit(dir, false); err != nil {
		t.Fatalf("first runInit: %v", err)
	}

	configPath := filepath.Join(dir, "config.yaml")
	if writeErr := os.WriteFile(configPath, []byte("# user-edited\n"), 0o644); writeErr != nil {
		t.Fatalf("seed user edit: %v", writeErr)
	}

	_, _, _, err := runInit(dir, false)
	if err == nil {
		t.Fatal("expected error when overwriting without --force, got nil")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("error %q does not mention overwrite refusal", err.Error())
	}

	// User edits must survive the refused overwrite.
	contents, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("read config after refused overwrite: %v", readErr)
	}
	if string(contents) != "# user-edited\n" {
		t.Errorf("expected user edits to be preserved; got %q", string(contents))
	}
}

func TestRunInit_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()

	if _, _, _, err := runInit(dir, false); err != nil {
		t.Fatalf("first runInit: %v", err)
	}

	configPath := filepath.Join(dir, "config.yaml")
	if writeErr := os.WriteFile(configPath, []byte("# stale\n"), 0o644); writeErr != nil {
		t.Fatalf("seed stale config: %v", writeErr)
	}

	if _, _, _, err := runInit(dir, true); err != nil {
		t.Fatalf("runInit --force: %v", err)
	}

	// Generated content should now be back, not the stale stub.
	contents, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("read regenerated config: %v", readErr)
	}
	if !strings.Contains(string(contents), "default_home_root:") {
		t.Errorf("regenerated config missing required default_home_root field; got: %s", string(contents))
	}
	if strings.Contains(string(contents), "stale") {
		t.Errorf("--force did not overwrite the stale stub; got: %s", string(contents))
	}
}
