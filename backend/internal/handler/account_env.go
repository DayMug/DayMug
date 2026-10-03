package handler

import (
	"errors"
	"fmt"
	"os"

	"github.com/DayMug/DayMug/backend/internal/config"
)

// ErrTerminalAccountNotFound is returned when the admin terminal is asked to
// start under an account name the config doesn't know.
var ErrTerminalAccountNotFound = errors.New("login account not configured")

// ensureAccountConfigDir creates the account's credentials directory if it is
// set but missing, so a first-time login has somewhere to write its auth file.
// An empty path is a no-op: Claude-compatible accounts may omit config_dir and
// fall back to the OS user's ~/.claude, which the CLI manages itself. Created
// 0o700 since the directory holds OAuth tokens.
var ensureAccountConfigDir = func(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create login config dir %q: %w", dir, err)
	}
	return nil
}

// buildAccountEnv composes the env for a CLI run under an account outside the
// chat path — the admin terminal child and the account status probes. Order of
// precedence (later wins): server env → per-account YAML env → configDirEnv
// (CLAUDE_CONFIG_DIR or CODEX_HOME) for the account → TERM.
//
// The browser renders output through a real terminal emulator (xterm.js), so
// the CLI should emit its normal interactive TUI: TERM=xterm-256color makes
// cursor positioning and colours render faithfully. NO_COLOR is stripped for
// the same reason.
//
// Kept local instead of reaching into the service package's chat-path env
// builders because those layer on extras these runs don't need
// (sandbox extras, MCP discovery).
func buildAccountEnv(acc *config.Provider, configDirEnv string) []string {
	env := os.Environ()
	env = removeEnvKV(env, "NO_COLOR")
	for k, v := range acc.Env {
		env = setEnvKV(env, k, v)
	}
	if acc.ConfigDir != "" {
		env = setEnvKV(env, configDirEnv, acc.ConfigDir)
	}
	env = setEnvKV(env, "TERM", "xterm-256color")
	return env
}

// setEnvKV replaces or appends key=value in env (later wins).
func setEnvKV(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func removeEnvKV(env []string, key string) []string {
	prefix := key + "="
	out := env[:0]
	for _, e := range env {
		if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
			continue
		}
		out = append(out, e)
	}
	return out
}
