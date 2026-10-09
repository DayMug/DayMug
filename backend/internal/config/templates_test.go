package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoTemplates are the config files operators actually copy. They drifted from
// the loader before (documented defaults that no longer matched Validate), and
// nothing caught it because nothing ever parsed them.
var repoTemplates = []string{
	filepath.Join("..", "..", "..", "config.example.yaml"),
	filepath.Join("..", "..", "..", "docs", "guides", "config.sample.yaml"),
}

func TestShippedTemplatesLoadAndValidate(t *testing.T) {
	for _, rel := range repoTemplates {
		t.Run(filepath.Base(rel), func(t *testing.T) {
			// Load resolves relative paths against the process cwd and expects
			// a writable-looking layout, so run it against a copy in TempDir
			// rather than the checkout.
			body, err := os.ReadFile(rel)
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatalf("write copy: %v", err)
			}
			if _, err := Load(path); err != nil {
				t.Fatalf("%s does not load: %v", rel, err)
			}
		})
	}
}

// Removed keys still load (the parser ignores them) but do nothing, so the
// templates must not advertise them: transports are chosen in the admin UI,
// providers live in the database, and guardrails no longer pause for approval.
func TestShippedTemplatesDoNotAdvertiseRemovedKeys(t *testing.T) {
	for _, rel := range repoTemplates {
		t.Run(filepath.Base(rel), func(t *testing.T) {
			body, err := os.ReadFile(rel)
			if err != nil {
				t.Fatalf("read %s: %v", rel, err)
			}
			for _, key := range []string{
				"claude_backend", "codex_backend", "runner_mode",
				"claude_accounts", "claude_config_dir",
				"model_calls_extension", "tool_calls_extension", "confirmation_timeout",
			} {
				if strings.Contains(string(body), key) {
					t.Errorf("%s mentions removed key %s", rel, key)
				}
			}
		})
	}
}
