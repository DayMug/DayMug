package sessionlog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClaudePathEncodesEveryNonAlnumRune(t *testing.T) {
	got := ClaudePath("/srv/work/.hidden/项目_x", "sid", "/cfg")
	want := filepath.Join("/cfg", "projects", "-srv-work--hidden----x", "sid.jsonl")
	if got != want {
		t.Fatalf("ClaudePath = %q, want %q", got, want)
	}
	if ClaudePath("", "sid", "/cfg") != "" || ClaudePath("/w", "", "/cfg") != "" {
		t.Fatal("ClaudePath must refuse an empty workDir or session id")
	}
}

func TestClaudeSessionExistsFollowsTheFile(t *testing.T) {
	cfg, work := t.TempDir(), t.TempDir()
	if (Claude{}).SessionExists(work, "sid", cfg) {
		t.Fatal("SessionExists before the log was written")
	}
	path := ClaudePath(work, "sid", cfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !(Claude{}).SessionExists(work, "sid", cfg) {
		t.Fatal("SessionExists missed the written log")
	}
}

func TestCodexRolloutFindsNestedRolloutUnderCodexHome(t *testing.T) {
	home := t.TempDir()
	day := filepath.Join(home, "sessions", "2026", "09", "25")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(day, "rollout-2026-09-25T08-00-00-sid-1.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (Codex{}).SessionLogPath("ignored", "sid-1", home); got != path {
		t.Fatalf("SessionLogPath = %q, want %q", got, path)
	}
	if (Codex{}).SessionExists("", "sid-2", home) || (Codex{}).SessionExists("", "", home) {
		t.Fatal("SessionExists matched an unrelated or empty id")
	}
}
