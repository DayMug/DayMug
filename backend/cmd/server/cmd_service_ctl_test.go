package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeResult struct {
	out string
	err error
}

// stubCommands routes runCommand to a script keyed by the full command line.
// Unscripted commands succeed with no output, so each test only spells out
// the calls that matter to it; the returned slice records every call made.
func stubCommands(t *testing.T, script map[string]fakeResult) *[]string {
	t.Helper()
	var calls []string
	orig := runCommand
	runCommand = func(name string, args ...string) ([]byte, error) {
		line := strings.Join(append([]string{name}, args...), " ")
		calls = append(calls, line)
		r := script[line]
		return []byte(r.out), r.err
	}
	t.Cleanup(func() { runCommand = orig })
	return &calls
}

func stubHealth(t *testing.T, err error) {
	t.Helper()
	orig := probeHealth
	probeHealth = func(context.Context, string) error { return err }
	t.Cleanup(func() { probeHealth = orig })
}

var errExit = errors.New("exit status 1")

func TestActivateLinuxUserService(t *testing.T) {
	const unit = "daymug.service"
	tests := []struct {
		name        string
		noStart     bool
		script      map[string]fakeResult
		wantCalls   []string
		wantStarted bool
		wantState   string
		wantProblem string
		wantManual  string
	}{
		{
			name: "fresh install enables and starts",
			script: map[string]fakeResult{
				"systemctl --user is-active " + unit: {out: "inactive\n", err: errExit},
			},
			wantCalls: []string{
				"systemctl --user daemon-reload",
				"systemctl --user enable " + unit,
				"systemctl --user is-active " + unit,
				"systemctl --user reset-failed " + unit,
				"systemctl --user start " + unit,
			},
			wantStarted: true,
			wantState:   "enabled, started",
		},
		{
			name: "re-install restarts the running service onto the new binary",
			script: map[string]fakeResult{
				"systemctl --user is-active " + unit: {out: "active\n"},
			},
			wantCalls: []string{
				"systemctl --user daemon-reload",
				"systemctl --user enable " + unit,
				"systemctl --user is-active " + unit,
				"systemctl --user restart " + unit,
			},
			wantStarted: true,
			wantState:   "restarted",
		},
		{
			name:        "--no-start only reloads",
			noStart:     true,
			wantCalls:   []string{"systemctl --user daemon-reload"},
			wantState:   "--no-start",
			wantManual:  "systemctl --user enable --now " + unit,
			wantStarted: false,
		},
		{
			name: "no user bus degrades to manual commands",
			script: map[string]fakeResult{
				"systemctl --user daemon-reload": {out: "Failed to connect to bus: No medium found\n", err: errExit},
			},
			wantCalls:   []string{"systemctl --user daemon-reload"},
			wantState:   "not started",
			wantProblem: "Failed to connect to bus",
			wantManual:  "systemctl --user enable --now " + unit,
		},
		{
			name: "start failure points at the journal",
			script: map[string]fakeResult{
				"systemctl --user is-active " + unit: {out: "failed\n", err: errExit},
				"systemctl --user start " + unit:     {out: "Job failed\n", err: errExit},
			},
			wantCalls: []string{
				"systemctl --user daemon-reload",
				"systemctl --user enable " + unit,
				"systemctl --user is-active " + unit,
				"systemctl --user reset-failed " + unit,
				"systemctl --user start " + unit,
			},
			wantState:   "not running",
			wantProblem: "Job failed",
			wantManual:  "journalctl --user -u " + unit,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := stubCommands(t, tt.script)
			got := activateLinuxUserService(tt.noStart)
			if !reflect.DeepEqual(*calls, tt.wantCalls) {
				t.Errorf("calls = %q\nwant    %q", *calls, tt.wantCalls)
			}
			if got.Started != tt.wantStarted {
				t.Errorf("Started = %v, want %v", got.Started, tt.wantStarted)
			}
			if !strings.Contains(got.State, tt.wantState) {
				t.Errorf("State = %q, want it to contain %q", got.State, tt.wantState)
			}
			if !strings.Contains(got.Problem, tt.wantProblem) || (tt.wantProblem == "" && got.Problem != "") {
				t.Errorf("Problem = %q, want %q", got.Problem, tt.wantProblem)
			}
			if tt.wantManual != "" && !strings.Contains(strings.Join(got.Manual, "\n"), tt.wantManual) {
				t.Errorf("Manual = %q, want one containing %q", got.Manual, tt.wantManual)
			}
		})
	}
}

func TestDetectLinger(t *testing.T) {
	tests := []struct {
		name     string
		username string
		result   fakeResult
		want     lingerState
	}{
		{"enabled", "alice", fakeResult{out: "Linger=yes\n"}, lingerOn},
		{"disabled", "alice", fakeResult{out: "Linger=no\n"}, lingerOff},
		{"loginctl fails", "alice", fakeResult{out: "Failed to get user\n", err: errExit}, lingerUnknown},
		{"unexpected output", "alice", fakeResult{out: "garbage"}, lingerUnknown},
		{"no username", "", fakeResult{}, lingerUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubCommands(t, map[string]fakeResult{
				"loginctl show-user " + tt.username + " -p Linger": tt.result,
			})
			if got := detectLinger(tt.username); got != tt.want {
				t.Errorf("detectLinger = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQueryLinuxUnit(t *testing.T) {
	const cmd = "systemctl --user show daymug.service -p LoadState -p UnitFileState -p ActiveState"
	stubCommands(t, map[string]fakeResult{
		cmd: {out: "LoadState=loaded\nUnitFileState=enabled\nActiveState=active\n"},
	})
	got, err := queryLinuxUnit()
	if err != nil {
		t.Fatalf("queryLinuxUnit: %v", err)
	}
	want := linuxUnitStatus{LoadState: "loaded", UnitFileState: "enabled", ActiveState: "active"}
	if got != want {
		t.Errorf("queryLinuxUnit = %+v, want %+v", got, want)
	}

	stubCommands(t, map[string]fakeResult{
		cmd: {out: "Failed to connect to bus\n", err: errExit},
	})
	if _, err := queryLinuxUnit(); err == nil || !strings.Contains(err.Error(), "bus") {
		t.Errorf("queryLinuxUnit error = %v, want the bus failure", err)
	}
}

func TestBrowseURL(t *testing.T) {
	tests := []struct{ addr, want string }{
		{"127.0.0.1:8090", "http://127.0.0.1:8090"},
		{":8090", "http://127.0.0.1:8090"},
		{"0.0.0.0:9000", "http://127.0.0.1:9000"},
		{"[::]:8090", "http://127.0.0.1:8090"},
		{"192.168.1.5:8090", "http://192.168.1.5:8090"},
		{"", "http://127.0.0.1:8080"},
	}
	for _, tt := range tests {
		if got := browseURL(tt.addr); got != tt.want {
			t.Errorf("browseURL(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}
