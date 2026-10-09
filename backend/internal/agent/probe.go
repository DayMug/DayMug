package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const codexInstallHint = "install or upgrade Codex with `npm install -g @openai/codex`, then verify `codex app-server --help`"

// ProbeCodexAppServerSupport verifies the only supported Codex chat transport
// before the server begins accepting requests. ResolveAgentBinary includes the
// same npm/nvm fallback search the real runner uses, so the probe and runtime
// cannot disagree merely because a service manager supplied a smaller PATH.
func ProbeCodexAppServerSupport(ctx context.Context) error {
	return probeCodexAppServerSupport(ctx, ResolveAgentBinary("codex"))
}

func probeCodexAppServerSupport(ctx context.Context, codexBinary string) error {
	if codexBinary == "" {
		return fmt.Errorf("codex executable not found; %s", codexInstallHint)
	}
	if !strings.ContainsRune(codexBinary, os.PathSeparator) {
		path, err := exec.LookPath(codexBinary)
		if err != nil {
			return fmt.Errorf("codex executable not found on PATH; %s", codexInstallHint)
		}
		codexBinary = path
	}

	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, codexBinary, "app-server", "--help")
	cmd.Stdout = io.Discard
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return fmt.Errorf("codex app-server is unavailable: %w (stderr: %s); %s", err, detail, codexInstallHint)
		}
		return fmt.Errorf("codex app-server is unavailable: %w; %s", err, codexInstallHint)
	}
	return nil
}

// ProbeClaudeConfigDirSupport verifies that the locally-installed `claude` CLI
// honors the CLAUDE_CONFIG_DIR environment variable. DayMug relies on this
// env to switch between Claude accounts at process spawn time; if the CLI
// ignored it, every account would silently share the server user's ~/.claude/.
//
// Probe procedure:
//  1. Create two empty temp dirs: one for CLAUDE_CONFIG_DIR, one for HOME.
//  2. Run `claude config list` with both env vars pointing at the temp dirs.
//  3. If CLAUDE_CONFIG_DIR is honored, claude writes `.claude.json` there.
//     If not, it falls back to HOME and the temp config dir stays empty.
//
// Returns nil iff CLAUDE_CONFIG_DIR is honored. Caller (server startup) is
// expected to fatal on error rather than fall back silently.
func ProbeClaudeConfigDirSupport(ctx context.Context) error {
	return probeClaudeConfigDirSupport(ctx, ResolveAgentBinary("claude"))
}

func probeClaudeConfigDirSupport(ctx context.Context, claudeBinary string) error {
	cfgDir, err := os.MkdirTemp("", "daymug-claude-probe-cfg-*")
	if err != nil {
		return fmt.Errorf("create probe cfg dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(cfgDir) }()

	home, err := os.MkdirTemp("", "daymug-claude-probe-home-*")
	if err != nil {
		return fmt.Errorf("create probe home dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(home) }()

	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, claudeBinary, "config", "list")
	// Strip parent env so HOME and CLAUDE_CONFIG_DIR cannot leak through.
	// PATH must be preserved so `claude` resolves; the rest is intentionally
	// minimal so the probe is hermetic.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"CLAUDE_CONFIG_DIR=" + cfgDir,
	}
	// We don't care about the output — only the side effect on the file
	// system. claude will print "Not logged in" which is fine.
	_ = cmd.Run()

	// Did claude write anything into the probe config dir? `.claude.json`
	// is the canonical marker; `projects/` and `sessions/` are also
	// created on the same code path. Any one of them is sufficient.
	for _, marker := range []string{".claude.json", "projects", "sessions"} {
		if _, err := os.Stat(filepath.Join(cfgDir, marker)); err == nil {
			return nil
		}
	}
	return fmt.Errorf("claude CLI did not honor CLAUDE_CONFIG_DIR (no artifacts written to probe dir); upgrade Claude Code to a version that supports this env var")
}
