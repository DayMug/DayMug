package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const showUnitCmd = "systemctl --user show daymug.service -p LoadState -p UnitFileState -p ActiveState"

func TestRuntimeCheck(t *testing.T) {
	tests := []struct {
		name  string
		p     prerequisites
		level checkLevel
		want  string
	}{
		{"both", prerequisites{NodeFound: true, AgentSDKFound: true, CodexFound: true, CodexAppServerFound: true}, checkPass, "Claude Agent SDK, Codex app-server"},
		{"claude only", prerequisites{NodeFound: true, AgentSDKFound: true}, checkPass, "codex providers won't run"},
		{"codex only", prerequisites{CodexFound: true, CodexAppServerFound: true}, checkPass, "claude providers won't run"},
		// The SDK alone is useless without Node to run it.
		{"sdk without node", prerequisites{AgentSDKFound: true}, checkFail, "neither"},
		{"codex without app-server", prerequisites{CodexFound: true}, checkFail, "neither"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runtimeCheck(tt.p)
			if got.Level != tt.level || !strings.Contains(got.Detail, tt.want) {
				t.Errorf("runtimeCheck = %+v, want level %v detail containing %q", got, tt.level, tt.want)
			}
			if got.Level == checkFail && got.Hint == "" {
				t.Error("a failed check must carry a fix hint")
			}
		})
	}
}

func writeTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(renderInitConfig(filepath.Join(dir, "users"))), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigCheck(t *testing.T) {
	t.Setenv("DAYMUG_ADDR", "")
	good := writeTestConfig(t)
	cfg, c := configCheck(good, "")
	if cfg == nil || c.Level != checkPass || c.Detail != good {
		t.Errorf("configCheck(valid) = %+v, cfg=%v", c, cfg)
	}

	bad := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(bad, []byte("server: [not a map"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, c = configCheck(bad, "")
	if cfg != nil || c.Level != checkFail || c.Hint == "" {
		t.Errorf("configCheck(invalid) = %+v, cfg=%v", c, cfg)
	}
}

// A binary outside ~/.daymug resolves no config of its own; doctor should
// then inspect the installed one rather than report "no config".
func TestConfigCheckFallsBackToInstalledConfig(t *testing.T) {
	t.Setenv("DAYMUG_ADDR", "")
	t.Setenv("DAYMUG_CONFIG", "")
	home := t.TempDir()
	installed := filepath.Join(home, installDirName, "config.yaml")
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte(renderInitConfig(filepath.Join(home, "users"))), 0o644); err != nil {
		t.Fatal(err)
	}
	if cfg, c := configCheck("", home); cfg == nil || c.Detail != installed {
		t.Errorf("configCheck fallback = %+v, want %s", c, installed)
	}
	if cfg, c := configCheck("", t.TempDir()); cfg != nil || c.Level != checkFail || !strings.Contains(c.Hint, "bootstrap") {
		t.Errorf("configCheck with nothing installed = %+v", c)
	}
}

func TestLinuxServiceChecks(t *testing.T) {
	type want struct {
		name   string
		level  checkLevel
		detail string
	}
	tests := []struct {
		name        string
		noManager   bool
		installUnit bool
		script      map[string]fakeResult
		want        []want
	}{
		{
			name:      "no systemctl",
			noManager: true,
			want:      []want{{"service", checkWarn, "systemctl not found"}},
		},
		{
			name: "unit not installed",
			want: []want{{"service", checkWarn, "no systemd --user unit"}},
		},
		{
			name:        "no user bus",
			installUnit: true,
			script:      map[string]fakeResult{showUnitCmd: {out: "Failed to connect to bus", err: errExit}},
			want:        []want{{"service", checkWarn, "unreachable"}},
		},
		{
			name:        "unit file not loaded",
			installUnit: true,
			script:      map[string]fakeResult{showUnitCmd: {out: "LoadState=not-found\nUnitFileState=\nActiveState=inactive"}},
			want:        []want{{"service", checkFail, "not loaded"}},
		},
		{
			name:        "healthy with linger",
			installUnit: true,
			script: map[string]fakeResult{
				showUnitCmd:                          {out: "LoadState=loaded\nUnitFileState=enabled\nActiveState=active"},
				"loginctl show-user alice -p Linger": {out: "Linger=yes"},
			},
			want: []want{
				{"service enabled", checkPass, "enabled"},
				{"service active", checkPass, "active"},
				{"linger", checkPass, "on"},
			},
		},
		{
			name:        "stopped, disabled, no linger",
			installUnit: true,
			script: map[string]fakeResult{
				showUnitCmd:                          {out: "LoadState=loaded\nUnitFileState=disabled\nActiveState=failed"},
				"loginctl show-user alice -p Linger": {out: "Linger=no"},
			},
			want: []want{
				{"service enabled", checkWarn, "disabled"},
				{"service active", checkFail, "failed"},
				{"linger", checkWarn, "off"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			if tt.installUnit {
				unit := systemdUserUnitPath(home)
				if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(unit, []byte("[Unit]\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			stubCommands(t, tt.script)
			got := linuxServiceChecks(doctorInput{
				Prereqs:  prerequisites{ServiceManagerAvailable: !tt.noManager},
				Home:     home,
				Username: "alice",
			})
			if len(got) != len(tt.want) {
				t.Fatalf("got %d checks %+v, want %d", len(got), got, len(tt.want))
			}
			for i, w := range tt.want {
				g := got[i]
				if g.Name != w.name || g.Level != w.level || !strings.Contains(g.Detail, w.detail) {
					t.Errorf("check %d = %+v, want %+v", i, g, w)
				}
				if g.Level != checkPass && g.Hint == "" {
					t.Errorf("check %q has no fix hint", g.Name)
				}
			}
		})
	}
}

// Linger off must name the exact command the operator runs; doctor never
// runs sudo itself.
func TestLinuxServiceChecksLingerHint(t *testing.T) {
	home := t.TempDir()
	unit := systemdUserUnitPath(home)
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unit, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := stubCommands(t, map[string]fakeResult{
		showUnitCmd:                        {out: "LoadState=loaded\nUnitFileState=enabled\nActiveState=active"},
		"loginctl show-user bob -p Linger": {out: "Linger=no"},
	})
	got := linuxServiceChecks(doctorInput{Prereqs: prerequisites{ServiceManagerAvailable: true}, Home: home, Username: "bob"})
	last := got[len(got)-1]
	if last.Hint != "sudo loginctl enable-linger bob" {
		t.Errorf("linger hint = %q", last.Hint)
	}
	for _, c := range *calls {
		if strings.HasPrefix(c, "sudo") {
			t.Errorf("doctor ran %q", c)
		}
	}
}

func TestMacServiceCheck(t *testing.T) {
	home := t.TempDir()
	stubCommands(t, nil)
	if got := macServiceCheck(doctorInput{Home: home}); got.Level != checkWarn {
		t.Errorf("missing plist: %+v, want warn", got)
	}

	plist := launchdPlistPath(home)
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := macServiceCheck(doctorInput{Home: home}); got.Level != checkPass {
		t.Errorf("loaded agent: %+v, want pass", got)
	}

	stubCommands(t, map[string]fakeResult{"launchctl print " + launchdTarget(): {err: errExit}})
	if got := macServiceCheck(doctorInput{Home: home}); got.Level != checkFail || !strings.Contains(got.Hint, "launchctl load -w") {
		t.Errorf("unloaded agent: %+v, want fail with load hint", got)
	}
}

func TestHealthCheck(t *testing.T) {
	t.Setenv("DAYMUG_ADDR", "")
	cfg, _ := configCheck(writeTestConfig(t), "")
	if cfg == nil {
		t.Fatal("test config did not load")
	}

	stubHealth(t, nil)
	if got := healthCheck(cfg); got.Level != checkPass || !strings.Contains(got.Detail, "http://127.0.0.1:8090") {
		t.Errorf("healthy: %+v", got)
	}

	stubHealth(t, errors.New("connection refused"))
	if got := healthCheck(cfg); got.Level != checkFail || !strings.Contains(got.Detail, "/api/health") {
		t.Errorf("down: %+v", got)
	}
}

func TestPrintDoctorChecklist(t *testing.T) {
	tests := []struct {
		name   string
		checks []doctorCheck
		wantOK bool
		want   []string
	}{
		{
			name:   "all pass",
			checks: []doctorCheck{{Name: "config", Detail: "/x/config.yaml"}},
			wantOK: true,
			want:   []string{"✓ config", "All good."},
		},
		{
			name:   "warnings do not fail",
			checks: []doctorCheck{{Level: checkWarn, Name: "linger", Detail: "off", Hint: "sudo loginctl enable-linger a"}},
			wantOK: true,
			want:   []string{"! linger", "fix: sudo loginctl enable-linger a", "1 warning(s)"},
		},
		{
			name: "a failure fails",
			checks: []doctorCheck{
				{Level: checkFail, Name: "server", Detail: "down", Hint: "start it"},
				{Level: checkWarn, Name: "linger", Detail: "off", Hint: "x"},
			},
			wantOK: false,
			want:   []string{"✗ server", "fix: start it", "1 problem(s) need fixing, 1 warning(s)."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if ok := printDoctorChecklist(&buf, tt.checks); ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			for _, w := range tt.want {
				if !strings.Contains(buf.String(), w) {
					t.Errorf("output missing %q:\n%s", w, buf.String())
				}
			}
		})
	}
}
