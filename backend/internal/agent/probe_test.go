package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeClaudeConfigDirSupport(t *testing.T) {
	t.Run("accepts CLI that honors config dir", func(t *testing.T) {
		binary := writeProbeCLI(t, `mkdir -p "$CLAUDE_CONFIG_DIR/projects"`)
		if err := probeClaudeConfigDirSupport(context.Background(), binary); err != nil {
			t.Fatalf("probe failed: %v", err)
		}
	})

	t.Run("rejects CLI that ignores config dir", func(t *testing.T) {
		binary := writeProbeCLI(t, ":")
		err := probeClaudeConfigDirSupport(context.Background(), binary)
		if err == nil || !strings.Contains(err.Error(), "did not honor CLAUDE_CONFIG_DIR") {
			t.Fatalf("error = %v, want unsupported-config-dir error", err)
		}
	})
}

func TestProbeCodexAppServerSupport(t *testing.T) {
	t.Run("accepts app-server subcommand", func(t *testing.T) {
		binary := writeProbeScript(t, `test "$1" = "app-server" && test "$2" = "--help"`)
		if err := probeCodexAppServerSupport(context.Background(), binary); err != nil {
			t.Fatalf("probe failed: %v", err)
		}
	})

	t.Run("missing binary includes install instructions", func(t *testing.T) {
		err := probeCodexAppServerSupport(context.Background(), "")
		if err == nil || !strings.Contains(err.Error(), "npm install -g @openai/codex") {
			t.Fatalf("error = %v, want Codex install hint", err)
		}
	})

	t.Run("old codex includes upgrade instructions", func(t *testing.T) {
		binary := writeProbeScript(t, `echo "unknown subcommand app-server" >&2; exit 2`)
		err := probeCodexAppServerSupport(context.Background(), binary)
		if err == nil || !strings.Contains(err.Error(), "app-server is unavailable") || !strings.Contains(err.Error(), "npm install -g @openai/codex") {
			t.Fatalf("error = %v, want app-server upgrade hint", err)
		}
	})
}

func writeProbeCLI(t *testing.T, action string) string {
	t.Helper()
	return writeProbeScript(t, "[ \"$1\" = config ] && [ \"$2\" = list ] || exit 2\n"+action)
}

func writeProbeScript(t *testing.T, action string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe-command")
	script := "#!/bin/sh\n" + action + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake command: %v", err)
	}
	return path
}
