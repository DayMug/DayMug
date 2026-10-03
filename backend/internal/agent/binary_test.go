package agent

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// resetAgentBinaryCache clears the per-process resolution cache so each
// table-driven case starts with a clean slate. Tests must call this any
// time they swap agentBinarySearchDirs or change PATH.
func resetAgentBinaryCache(t *testing.T) {
	t.Helper()
	agentBinaryOnce = sync.Map{}
	agentBinaryCache = sync.Map{}
}

func TestResolveAgentBinary_PassThroughForPaths(t *testing.T) {
	resetAgentBinaryCache(t)
	// Absolute paths and any name containing a separator should be returned
	// untouched — that's the explicit-override seam tests and operators rely
	// on; running it through LookPath would defeat the point.
	cases := []string{"/opt/bin/codex", "./codex", "sub/dir/codex"}
	for _, in := range cases {
		if got := ResolveAgentBinary(in); got != in {
			t.Errorf("ResolveAgentBinary(%q) = %q, want %q", in, got, in)
		}
	}
}

func TestResolveAgentBinary_EmptyNamePassThrough(t *testing.T) {
	resetAgentBinaryCache(t)
	if got := ResolveAgentBinary(""); got != "" {
		t.Errorf("ResolveAgentBinary(\"\") = %q, want empty", got)
	}
}

func TestResolveAgentBinary_FallsBackToScanDirs(t *testing.T) {
	resetAgentBinaryCache(t)
	tmp := t.TempDir()
	// Drop a fake binary into a scan dir; PATH does not contain tmp so
	// LookPath will miss, exercising the scan fallback.
	bin := filepath.Join(tmp, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", "")
	orig := agentBinarySearchDirs
	agentBinarySearchDirs = func() []string { return []string{tmp} }
	t.Cleanup(func() { agentBinarySearchDirs = orig })

	if got := ResolveAgentBinary("codex"); got != bin {
		t.Errorf("ResolveAgentBinary fallback = %q, want %q", got, bin)
	}
}

func TestResolveAgentBinary_ReturnsBareNameWhenNothingFound(t *testing.T) {
	resetAgentBinaryCache(t)
	t.Setenv("PATH", "")
	orig := agentBinarySearchDirs
	agentBinarySearchDirs = func() []string { return []string{t.TempDir()} }
	t.Cleanup(func() { agentBinarySearchDirs = orig })

	// No hit anywhere — caller still gets the bare name so the eventual
	// exec error surfaces the familiar "not found" message.
	if got := ResolveAgentBinary("codex"); got != "codex" {
		t.Errorf("ResolveAgentBinary not-found = %q, want %q", got, "codex")
	}
}

func TestResolveAgentBinary_CachesPerName(t *testing.T) {
	resetAgentBinaryCache(t)
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")

	calls := 0
	orig := agentBinarySearchDirs
	agentBinarySearchDirs = func() []string {
		calls++
		return []string{tmp}
	}
	t.Cleanup(func() { agentBinarySearchDirs = orig })

	for i := 0; i < 3; i++ {
		_ = ResolveAgentBinary("codex")
	}
	if calls != 1 {
		t.Errorf("agentBinarySearchDirs called %d times, want 1 (result should be cached)", calls)
	}
}

func TestResolveAgentBinary_RefreshesStaleCachedPath(t *testing.T) {
	resetAgentBinaryCache(t)
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")

	orig := agentBinarySearchDirs
	agentBinarySearchDirs = func() []string { return []string{tmp} }
	t.Cleanup(func() { agentBinarySearchDirs = orig })

	if got := ResolveAgentBinary("codex"); got != bin {
		t.Fatalf("initial ResolveAgentBinary = %q, want %q", got, bin)
	}
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	replacementDir := t.TempDir()
	replacement := filepath.Join(replacementDir, "codex")
	if err := os.WriteFile(replacement, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	agentBinarySearchDirs = func() []string { return []string{replacementDir} }

	if got := ResolveAgentBinary("codex"); got != replacement {
		t.Errorf("ResolveAgentBinary after stale cache = %q, want %q", got, replacement)
	}
}

func TestResolveAgentBinary_RefreshesAfterNotFound(t *testing.T) {
	resetAgentBinaryCache(t)
	tmp := t.TempDir()
	t.Setenv("PATH", "")

	orig := agentBinarySearchDirs
	agentBinarySearchDirs = func() []string { return []string{tmp} }
	t.Cleanup(func() { agentBinarySearchDirs = orig })

	if got := ResolveAgentBinary("codex"); got != "codex" {
		t.Fatalf("initial ResolveAgentBinary = %q, want bare name", got)
	}
	bin := filepath.Join(tmp, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := ResolveAgentBinary("codex"); got != bin {
		t.Errorf("ResolveAgentBinary after install = %q, want %q", got, bin)
	}
}

func TestNvmBinDirs_HighestVersionWins(t *testing.T) {
	home := t.TempDir()
	for _, v := range []string{"v9.0.0", "v24.15.0", "v22.5.1"} {
		if err := os.MkdirAll(filepath.Join(home, ".nvm", "versions", "node", v, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	dirs := nvmBinDirs(home)
	if len(dirs) != 3 {
		t.Fatalf("len(dirs) = %d, want 3 (%v)", len(dirs), dirs)
	}
	// Lex sort would put v9 first; numeric sort must put v24 first so a
	// freshly-installed node beats an ancient one. v22 in the middle proves
	// we don't trip on the minor/patch components.
	want := []string{"v24.15.0", "v22.5.1", "v9.0.0"}
	for i, v := range want {
		got := filepath.Base(filepath.Dir(dirs[i]))
		if got != v {
			t.Errorf("dirs[%d] resolved version = %q, want %q (all=%v)", i, got, v, dirs)
		}
	}
}

func TestNvmBinDirs_DefaultAliasPromoted(t *testing.T) {
	home := t.TempDir()
	for _, v := range []string{"v22.5.1", "v24.15.0"} {
		if err := os.MkdirAll(filepath.Join(home, ".nvm", "versions", "node", v, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	aliasDir := filepath.Join(home, ".nvm", "alias")
	if err := os.MkdirAll(aliasDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// nvm typically writes the major number ("24"); resolveNvmDefault must
	// expand that to the matching installed v24.x.y directory rather than
	// blindly trusting the semver-desc order.
	if err := os.WriteFile(filepath.Join(aliasDir, "default"), []byte("22\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirs := nvmBinDirs(home)
	if len(dirs) != 2 {
		t.Fatalf("len(dirs) = %d, want 2", len(dirs))
	}
	if got := filepath.Base(filepath.Dir(dirs[0])); got != "v22.5.1" {
		t.Errorf("default alias not promoted: dirs[0] = %q, want v22.5.1", got)
	}
}

func TestNvmBinDirs_MissingRootReturnsNil(t *testing.T) {
	if got := nvmBinDirs(t.TempDir()); got != nil {
		t.Errorf("nvmBinDirs(empty home) = %v, want nil", got)
	}
}

func TestCompareNvmVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v24.15.0", "v9.0.0", 1},
		{"v22.5.1", "v22.5.1", 0},
		{"v24.0.0", "v24.0.1", -1},
		{"v20.10.0", "v20.9.9", 1},
		{"v20.0.0-rc.1", "v20.0.0", 0}, // pre-release suffix dropped, numeric prefix wins
	}
	for _, c := range cases {
		if got := compareNvmVersion(c.a, c.b); got != c.want {
			t.Errorf("compareNvmVersion(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
