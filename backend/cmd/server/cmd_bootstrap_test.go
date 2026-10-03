package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintPrerequisiteReport_LinuxAllFound(t *testing.T) {
	var buf bytes.Buffer
	printPrerequisiteReport(&buf, prerequisites{
		OS:                      "linux",
		NodeFound:               true,
		NodePath:                "/usr/bin/node",
		AgentSDKFound:           true,
		AgentSDKPath:            "/usr/lib/node_modules/@anthropic-ai/claude-agent-sdk/sdk.mjs",
		AgentSDKVersion:         "0.1.0",
		CodexFound:              true,
		CodexPath:               "/home/u/.local/bin/codex",
		CodexAppServerFound:     true,
		ServiceManagerAvailable: true,
		ServiceManagerKind:      "systemd --user",
	})
	out := buf.String()
	for _, want := range []string{
		"Pre-flight",
		"claude-agent-sdk:  0.1.0 /usr/lib/node_modules/@anthropic-ai/claude-agent-sdk/sdk.mjs",
		"codex:             /home/u/.local/bin/codex",
		"codex app-server",
		"systemd --user",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\nfull:\n%s", want, out)
		}
	}
	if strings.Contains(out, "not found") {
		t.Errorf("report should not flag any missing item; got:\n%s", out)
	}
}

func TestPrintPrerequisiteReport_LinuxCodexMissing(t *testing.T) {
	var buf bytes.Buffer
	printPrerequisiteReport(&buf, prerequisites{
		OS:                      "linux",
		NodeFound:               true,
		NodePath:                "/usr/bin/node",
		AgentSDKFound:           true,
		AgentSDKPath:            "/sdk/sdk.mjs",
		ServiceManagerAvailable: true,
		ServiceManagerKind:      "systemd --user",
	})
	out := buf.String()
	for _, want := range []string{
		"codex:             not found",
		"npm install -g @openai/codex",
		"codex app-server --help",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestPrintPrerequisiteReport_DarwinFound(t *testing.T) {
	var buf bytes.Buffer
	printPrerequisiteReport(&buf, prerequisites{
		OS:                      "darwin",
		NodeFound:               true,
		NodePath:                "/opt/homebrew/bin/node",
		AgentSDKFound:           true,
		AgentSDKPath:            "/opt/homebrew/lib/node_modules/@anthropic-ai/claude-agent-sdk/sdk.mjs",
		CodexFound:              true,
		CodexPath:               "/opt/homebrew/bin/codex",
		CodexAppServerFound:     true,
		ServiceManagerAvailable: true,
		ServiceManagerKind:      "launchd",
	})
	out := buf.String()
	if !strings.Contains(out, "launchd") {
		t.Errorf("darwin report missing launchd line; got:\n%s", out)
	}
}

func TestPrintPrerequisiteReport_DarwinNoLaunchctl(t *testing.T) {
	var buf bytes.Buffer
	printPrerequisiteReport(&buf, prerequisites{
		OS:                      "darwin",
		NodeFound:               true,
		AgentSDKFound:           true,
		ServiceManagerAvailable: false,
	})
	out := buf.String()
	if !strings.Contains(out, "launchctl not found") {
		t.Errorf("expected launchctl-missing notice; got:\n%s", out)
	}
}

// The Agent SDK is the only Claude transport, so a missing package must have an
// installation instruction rather than the removed CLI fallback.
func TestPrintPrerequisiteReport_AgentSDKMissing(t *testing.T) {
	var buf bytes.Buffer
	printPrerequisiteReport(&buf, prerequisites{
		OS:                      "linux",
		NodeFound:               true,
		NodePath:                "/usr/bin/node",
		AgentSDKFound:           false,
		ServiceManagerAvailable: true,
		ServiceManagerKind:      "systemd --user",
	})
	out := buf.String()
	for _, want := range []string{
		"claude-agent-sdk:  not found",
		"npm install -g @anthropic-ai/claude-agent-sdk",
		"there is no CLI fallback",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestPrintPrerequisiteReport_NodeMissing(t *testing.T) {
	var buf bytes.Buffer
	printPrerequisiteReport(&buf, prerequisites{
		OS:                      "linux",
		NodeFound:               false,
		ServiceManagerAvailable: true,
		ServiceManagerKind:      "systemd --user",
	})
	out := buf.String()
	if !strings.Contains(out, "node:              not found on PATH (Node.js 18+ required)") {
		t.Errorf("expected node-missing notice; got:\n%s", out)
	}
}

func TestPrintPrerequisiteReport_CodexWithoutAppServer(t *testing.T) {
	var buf bytes.Buffer
	printPrerequisiteReport(&buf, prerequisites{
		OS:                      "linux",
		NodeFound:               true,
		NodePath:                "/usr/bin/node",
		AgentSDKFound:           true,
		AgentSDKPath:            "/sdk/sdk.mjs",
		CodexFound:              true,
		CodexPath:               "/usr/bin/codex",
		CodexAppServerError:     "unknown subcommand app-server",
		ServiceManagerAvailable: true,
		ServiceManagerKind:      "systemd --user",
	})
	out := buf.String()
	for _, want := range []string{"codex app-server:  unavailable", "npm install -g @openai/codex", "unknown subcommand app-server"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestPrintBootstrapSummary(t *testing.T) {
	base := bootstrapSummary{
		ExecPath:   "/home/alice/.daymug/daymug",
		ConfigPath: "/home/alice/.daymug/config.yaml",
		URL:        "http://127.0.0.1:8090",
	}
	tests := []struct {
		name    string
		mod     func(*bootstrapSummary)
		want    []string
		notWant []string
	}{
		{
			name: "started and healthy, linger on",
			mod: func(s *bootstrapSummary) {
				s.ServiceName = "systemd --user daymug.service"
				s.Service = serviceOutcome{State: "enabled, started", Started: true}
				s.HealthChecked, s.Healthy = true, true
				s.Linger = lingerOn
			},
			want: []string{
				"service:  systemd --user daymug.service: enabled, started",
				"URL:      http://127.0.0.1:8090\n",
				"Troubleshoot: /home/alice/.daymug/daymug doctor",
			},
			notWant: []string{"Linger", "not answering", "To start DayMug"},
		},
		{
			name: "linger off prints the sudo command",
			mod: func(s *bootstrapSummary) {
				s.Service = serviceOutcome{State: "enabled, started", Started: true}
				s.LingerUser, s.Linger = "alice", lingerOff
			},
			want: []string{"Linger is off for alice", "sudo loginctl enable-linger alice"},
		},
		{
			name: "started but not answering",
			mod: func(s *bootstrapSummary) {
				s.Service = serviceOutcome{State: "enabled, started", Started: true}
				s.HealthChecked = true
			},
			want: []string{"http://127.0.0.1:8090 (not answering yet)"},
		},
		{
			name: "degraded service lists manual commands",
			mod: func(s *bootstrapSummary) {
				s.Service = serviceOutcome{
					State:   "unit written, not started",
					Problem: "systemctl --user is unavailable",
					Manual:  []string{"systemctl --user enable --now daymug.service"},
				}
			},
			want: []string{"! systemctl --user is unavailable", "To start DayMug, run:\n  systemctl --user enable --now daymug.service"},
		},
		{
			name: "unloadable config",
			mod: func(s *bootstrapSummary) {
				s.Service = serviceOutcome{State: "not installed (--skip-service)", Manual: []string{"/home/alice/.daymug/daymug serve"}}
				s.URL, s.ConfigErr = "", "parse config: bad"
			},
			want: []string{"service:  not installed (--skip-service)", "URL:      unknown, config does not load: parse config: bad", "/home/alice/.daymug/daymug serve"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := base
			tt.mod(&s)
			var buf bytes.Buffer
			printBootstrapSummary(&buf, s)
			out := buf.String()
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("summary missing %q:\n%s", w, out)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(out, w) {
					t.Errorf("summary should not contain %q:\n%s", w, out)
				}
			}
		})
	}
}
