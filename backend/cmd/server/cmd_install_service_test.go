package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderUserServiceUnit(t *testing.T) {
	out, err := renderUserServiceUnit(unitFields{
		ExecPath: "/home/alice/.daymug/daymug",
		WorkDir:  "/home/alice/.daymug",
		Path:     "/home/alice/.local/bin:/usr/local/bin:/usr/bin:/bin",
	})
	if err != nil {
		t.Fatalf("renderUserServiceUnit: %v", err)
	}

	wantSubstrings := []string{
		"Description=DayMug",
		// No --config flag — config.yaml auto-resolves from binary dir.
		"ExecStart=/home/alice/.daymug/daymug serve",
		"WorkingDirectory=/home/alice/.daymug",
		"Restart=on-failure",
		// User units must target default.target, not multi-user.target.
		"WantedBy=default.target",
		// PATH must be pinned so systemd --user can find Node and Codex.
		"Environment=PATH=/home/alice/.local/bin:/usr/local/bin:/usr/bin:/bin",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(out, want) {
			t.Errorf("unit missing %q\nrendered:\n%s", want, out)
		}
	}
	if strings.Contains(out, "multi-user.target") {
		t.Errorf("unit should not target multi-user.target (system-only):\n%s", out)
	}
	if strings.Contains(out, "--config") {
		t.Errorf("unit should not pass --config (auto-resolved):\n%s", out)
	}
}

// A binary that fatals on every start (missing Agent SDK, unreachable OIDC
// issuer) must eventually land in `failed` instead of respawning forever, so
// the start limiter has to be present *and* sit in [Unit] — systemd ≥ 229
// ignores these keys under [Service].
func TestRenderUserServiceUnitBoundsRestartLoop(t *testing.T) {
	out, err := renderUserServiceUnit(unitFields{
		ExecPath: "/home/alice/.daymug/daymug",
		WorkDir:  "/home/alice/.daymug",
		Path:     "/usr/bin:/bin",
	})
	if err != nil {
		t.Fatalf("renderUserServiceUnit: %v", err)
	}

	unitSection, _, found := strings.Cut(out, "[Service]")
	if !found {
		t.Fatalf("unit has no [Service] section:\n%s", out)
	}
	for _, want := range []string{"StartLimitIntervalSec=300", "StartLimitBurst=5"} {
		if !strings.Contains(unitSection, want) {
			t.Errorf("[Unit] section missing %q\nrendered:\n%s", want, out)
		}
	}
	// The limiter is only meaningful together with the restart policy it bounds.
	if !strings.Contains(out, "Restart=on-failure") {
		t.Errorf("unit lost Restart=on-failure:\n%s", out)
	}
}

func TestLaunchdPlistPathUsesPerUserAutoStartDirectory(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "Users", "alice")
	want := filepath.Join(home, "Library", "LaunchAgents", launchdAgentLabel+".plist")
	if got := launchdPlistPath(home); got != want {
		t.Errorf("plist path = %q, want %q", got, want)
	}
}

func TestRenderLaunchdPlist(t *testing.T) {
	out, err := renderLaunchdPlist(plistFields{
		Label:     "com.daymug.daymug",
		ExecPath:  "/Users/alice/.daymug/daymug",
		WorkDir:   "/Users/alice/.daymug",
		StdoutLog: "/Users/alice/.daymug/logs/daymug.out.log",
		StderrLog: "/Users/alice/.daymug/logs/daymug.err.log",
		Path:      "/Users/alice/.npm-global/bin:/usr/local/bin:/usr/bin:/bin",
	})
	if err != nil {
		t.Fatalf("renderLaunchdPlist: %v", err)
	}

	wantSubstrings := []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		"<string>com.daymug.daymug</string>",
		"<string>/Users/alice/.daymug/daymug</string>",
		"<string>serve</string>",
		"<string>/Users/alice/.daymug</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<string>/Users/alice/.daymug/logs/daymug.out.log</string>",
		"<string>/Users/alice/.daymug/logs/daymug.err.log</string>",
		// PATH must land in the EnvironmentVariables dict.
		"<key>PATH</key>",
		"<string>/Users/alice/.npm-global/bin:/usr/local/bin:/usr/bin:/bin</string>",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(out, want) {
			t.Errorf("plist missing %q\nrendered:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--config") {
		t.Errorf("plist should not pass --config (auto-resolved):\n%s", out)
	}
}

func TestRenderUserServiceUnit_EmptyFields(t *testing.T) {
	// Templates should not panic on empty fields; the caller is responsible
	// for guarding against empty paths upstream, but we want a stable error
	// shape rather than a runtime crash.
	_, err := renderUserServiceUnit(unitFields{})
	if err != nil {
		t.Fatalf("unexpected render error on empty fields: %v", err)
	}
}
