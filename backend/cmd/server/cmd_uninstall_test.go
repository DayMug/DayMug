package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCheckPurgeTarget(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home", "alice")
	install := filepath.Join(home, ".daymug")
	checkout := filepath.Join(home, "src", "daymug")
	bare := filepath.Join(home, "bin")
	for _, d := range []string{install, filepath.Join(checkout, ".git"), bare} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{install, checkout, home} {
		if err := os.WriteFile(filepath.Join(d, "config.yaml"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name    string
		dir     string
		wantErr string
	}{
		{"install dir", install, ""},
		{"install dir with trailing slash", install + "/", ""},
		{"home itself", home, "home directory"},
		{"parent of home", filepath.Dir(home), "home directory"},
		{"filesystem root", "/", "root"},
		{"relative path", ".daymug", "absolute"},
		{"no config.yaml", bare, "does not look like"},
		{"source checkout", checkout, "git checkout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkPurgeTarget(tt.dir, home)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("checkPurgeTarget(%q) = %v, want nil", tt.dir, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("checkPurgeTarget(%q) = %v, want error containing %q", tt.dir, err, tt.wantErr)
			}
		})
	}
}

func TestReadConfirmation(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"y\n", true},
		{"YES\n", true},
		{" yes \n", true},
		{"n\n", false},
		{"\n", false},
		{"", false},
		{"yep\n", false},
	}
	for _, tt := range tests {
		if got := readConfirmation(strings.NewReader(tt.in)); got != tt.want {
			t.Errorf("readConfirmation(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestRemoveLinuxUserService(t *testing.T) {
	const unit = "daymug.service"
	tests := []struct {
		name      string
		install   bool
		script    map[string]fakeResult
		wantCalls []string
		wantOut   string
	}{
		{
			name:    "stops, disables, removes and reloads",
			install: true,
			wantCalls: []string{
				"systemctl --user disable --now " + unit,
				"systemctl --user daemon-reload",
				"systemctl --user reset-failed " + unit,
			},
			wantOut: "Removed",
		},
		{
			name:    "no user bus still removes the unit file",
			install: true,
			script: map[string]fakeResult{
				"systemctl --user disable --now " + unit: {out: "Failed to connect to bus", err: errExit},
			},
			wantCalls: []string{"systemctl --user disable --now " + unit},
			wantOut:   "stop it from a login session",
		},
		{
			name:      "nothing installed",
			wantCalls: nil,
			wantOut:   "No systemd --user unit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			unitPath := systemdUserUnitPath(home)
			dropIn := filepath.Join(unitPath+".d", "50-start-limit.conf")
			if tt.install {
				if err := os.MkdirAll(filepath.Dir(dropIn), 0o755); err != nil {
					t.Fatal(err)
				}
				for _, p := range []string{unitPath, dropIn} {
					if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			calls := stubCommands(t, tt.script)
			var out bytes.Buffer
			removeLinuxUserService(&out, home)

			if !reflect.DeepEqual(*calls, tt.wantCalls) {
				t.Errorf("calls = %q, want %q", *calls, tt.wantCalls)
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("output %q missing %q", out.String(), tt.wantOut)
			}
			for _, p := range []string{unitPath, unitPath + ".d"} {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Errorf("%s still exists (err=%v)", p, err)
				}
			}
		})
	}
}

func TestRemoveMacLaunchAgent(t *testing.T) {
	home := t.TempDir()
	plist := launchdPlistPath(home)
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, installDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := stubCommands(t, nil)
	var out bytes.Buffer
	removeMacLaunchAgent(&out, home)

	want := []string{"launchctl unload -w " + plist}
	if !reflect.DeepEqual(*calls, want) {
		t.Errorf("calls = %q, want %q", *calls, want)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Errorf("%s still exists", plist)
	}
	// The install dir itself (data, config) is untouched without --purge.
	if _, err := os.Stat(filepath.Join(home, installDirName)); err != nil {
		t.Errorf("install dir removed: %v", err)
	}
}
