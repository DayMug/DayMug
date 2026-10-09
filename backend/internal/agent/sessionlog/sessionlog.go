// Package sessionlog locates the session logs Claude Code and Codex write to
// disk. The layout belongs to the provider's client, not to the transport that
// drove it: the Claude CLI and the Agent SDK write the same projects/*.jsonl,
// `codex exec` and the Codex app-server the same rollout JSONL. Keeping the
// probes here lets every transport answer SessionExists / SessionLogPath
// without constructing another transport's whole Backend to borrow them.
package sessionlog

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ClaudeProjectsEncoder mirrors how Claude Code maps a cwd to a directory
// name under ~/.claude/projects. Empirically verified against Claude Code
// 2.1.131/133 (Node) on Linux: every rune outside [A-Za-z0-9-] collapses to
// "-" — so "/", ".", "_", and CJK characters all become a single dash.
// Mirroring "/" alone (the previous logic) silently produced the wrong
// directory for any workDir containing a hidden segment or non-ASCII rune,
// so SessionExists missed the on-disk jsonl and the next send failed with
// "Session ID is already in use".
var ClaudeProjectsEncoder = regexp.MustCompile(`[^A-Za-z0-9-]`)

// ClaudePath returns <configDir>/projects/<encoded-cwd>/<sessionID>.jsonl,
// the per-cwd log Claude Code writes on its first --session-id call. Claude
// derives the location from workDir alone, so the path is predictable before
// the file exists — which lets the cancel path snapshot the pre-turn size and
// roll back without parsing the JSONL. An empty configDir means ~/.claude,
// mirroring the CLAUDE_CONFIG_DIR value the child process receives.
func ClaudePath(workDir, sessionID, configDir string) string {
	if workDir == "" || sessionID == "" {
		return ""
	}
	base := configDir
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".claude")
	}
	encoded := ClaudeProjectsEncoder.ReplaceAllString(filepath.Clean(workDir), "-")
	return filepath.Join(base, "projects", encoded, sessionID+".jsonl")
}

// Claude answers the agent.Backend session probes for Claude Code sessions.
type Claude struct{}

func (Claude) SessionExists(workDir, sessionID, configDir string) bool {
	path := ClaudePath(workDir, sessionID, configDir)
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func (Claude) SessionLogPath(workDir, sessionID, configDir string) string {
	return ClaudePath(workDir, sessionID, configDir)
}

// CodexSessionsDir is where Codex keeps its rollouts when CODEX_HOME is
// unset (~/.codex/sessions). A variable so tests can point it at a temp dir.
var CodexSessionsDir = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

// CodexSessionsRoot resolves the sessions root for a per-account CODEX_HOME
// (carried as agent.RunRequest.ConfigDir). Empty codexHome falls back to
// CodexSessionsDir so the default account keeps ~/.codex/sessions.
func CodexSessionsRoot(codexHome string) string {
	if codexHome != "" {
		return filepath.Join(codexHome, "sessions")
	}
	return CodexSessionsDir()
}

// CodexRollout walks <CODEX_HOME>/sessions/**/rollout-*-<sid>.jsonl and
// returns the matching rollout's path, or "" when none exists. Codex picks
// its own session id and lays the file under a YYYY/MM/DD tree, so unlike
// Claude the path cannot be predicted before the file lands; a fresh walk
// costs at most a directory listing per day the user has been active.
func CodexRollout(sessionID, codexHome string) string {
	if sessionID == "" {
		return ""
	}
	root := CodexSessionsRoot(codexHome)
	if root == "" {
		return ""
	}
	suffix := "-" + sessionID + ".jsonl"
	var found string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Missing root or transient read error → no session.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), suffix) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// Codex answers the agent.Backend session probes for Codex sessions. workDir
// is ignored: rollouts are keyed by session id, not cwd.
type Codex struct{}

func (Codex) SessionExists(_, sessionID, codexHome string) bool {
	return CodexRollout(sessionID, codexHome) != ""
}

func (Codex) SessionLogPath(_, sessionID, codexHome string) string {
	return CodexRollout(sessionID, codexHome)
}
