package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
)

func TestNoopSandboxPassesThrough(t *testing.T) {
	s, err := NewSandbox(nil)
	if err != nil {
		t.Fatalf("NewSandbox(nil): %v", err)
	}
	argv := []string{"claude", "-p", "--print"}
	out, env, err := s.Wrap(argv, "/work", WrapOpts{})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if len(out) != len(argv) {
		t.Errorf("argv len changed: got %d want %d", len(out), len(argv))
	}
	for i := range argv {
		if out[i] != argv[i] {
			t.Errorf("argv[%d]: got %q want %q", i, out[i], argv[i])
		}
	}
	if len(env) != 0 {
		t.Errorf("noop should not inject env, got %v", env)
	}
}

func TestNewSandbox_DisabledReturnsNoop(t *testing.T) {
	s, err := NewSandbox(&config.SandboxConfig{Enabled: false})
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	if _, ok := s.(noopSandbox); !ok {
		t.Errorf("expected noopSandbox when disabled, got %T", s)
	}
}

func TestNewSandbox_NoopTypeReturnsNoop(t *testing.T) {
	s, err := NewSandbox(&config.SandboxConfig{Enabled: true, Type: config.SandboxNoop})
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	if _, ok := s.(noopSandbox); !ok {
		t.Errorf("expected noopSandbox when type=noop, got %T", s)
	}
}

func TestSandboxIsolatesProcess(t *testing.T) {
	noop, err := NewSandbox(nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name         string
		sandbox      Sandbox
		unrestricted bool
		want         bool
	}{
		{name: "nil", want: false},
		{name: "noop", sandbox: noop, want: false},
		{name: "bwrap", sandbox: bwrapSandbox{}, want: true},
		{name: "bwrap bypassed", sandbox: bwrapSandbox{}, unrestricted: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SandboxIsolatesProcess(tc.sandbox, tc.unrestricted); got != tc.want {
				t.Fatalf("SandboxIsolatesProcess = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestBwrapSandbox_UnrestrictedPassesThrough(t *testing.T) {
	s := bwrapSandbox{bwrapPath: "/usr/bin/bwrap"}
	argv := []string{"/opt/claude", "-p"}
	out, env, err := s.Wrap(argv, "/work/u", WrapOpts{Unrestricted: true, JailRoot: "/work/u"})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if !slices.Equal(out, argv) {
		t.Errorf("unrestricted should pass argv through unchanged, got %v", out)
	}
	if len(env) != 0 {
		t.Errorf("expected no extra env, got %v", env)
	}
}

func TestBwrapSandbox_JailedWrapsArgv(t *testing.T) {
	s := bwrapSandbox{bwrapPath: "/usr/bin/bwrap", cfg: config.SandboxConfig{Network: true}}
	argv := []string{"/opt/claude/bin/claude", "-p"}
	out, _, err := s.Wrap(argv, "/work/u", WrapOpts{JailRoot: "/work/u", ConfigDir: "/cfg/u"})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if out[0] != "/usr/bin/bwrap" {
		t.Fatalf("argv[0] should be bwrap, got %q", out[0])
	}
	joined := strings.Join(out, " ")
	// The jail root is the only read-write bind of the work tree.
	if !strings.Contains(joined, "--bind /work/u /work/u") {
		t.Errorf("missing rw bind for jail root: %v", out)
	}
	if !strings.Contains(joined, "--bind /cfg/u /cfg/u") {
		t.Errorf("missing rw bind for config dir: %v", out)
	}
	if !strings.Contains(joined, "--unshare-all") || !strings.Contains(joined, "--share-net") {
		t.Errorf("expected --unshare-all + --share-net (network: true): %v", out)
	}
	if !strings.Contains(joined, "--setenv HOME /work/u") {
		t.Errorf("expected HOME pinned to jail: %v", out)
	}
	if strings.Contains(joined, "--ro-bind-try /etc/resolv.conf /etc/resolv.conf") {
		t.Errorf("resolv.conf symlink must never be used as a mount destination: %v", out)
	}
	// Original command must follow the -- separator intact.
	sep := slices.Index(out, "--")
	if sep < 0 || !slices.Equal(out[sep+1:], argv) {
		t.Errorf("original argv not preserved after separator: %v", out)
	}
}

func TestBwrapSandbox_NoNetworkOmitsShareNet(t *testing.T) {
	s := bwrapSandbox{bwrapPath: "/usr/bin/bwrap", cfg: config.SandboxConfig{Network: false}}
	out, _, err := s.Wrap([]string{"/opt/claude", "-p"}, "/work/u", WrapOpts{JailRoot: "/work/u"})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if slices.Contains(out, "--share-net") {
		t.Errorf("network: false should not re-share net: %v", out)
	}
}

func TestBwrapSandbox_JailedRequiresWorkDir(t *testing.T) {
	s := bwrapSandbox{bwrapPath: "/usr/bin/bwrap"}
	if _, _, err := s.Wrap([]string{"/opt/claude"}, "", WrapOpts{}); err == nil {
		t.Error("expected error when a jailed invocation has no work directory")
	}
}

func TestResolveToolDirs_FollowsSymlinkTarget(t *testing.T) {
	// Mirror claude's install: a launcher symlink on PATH pointing at a
	// versioned binary in a different directory. Both dirs must be bound,
	// else execvp inside the jail can't resolve the symlink target.
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	verDir := filepath.Join(root, "share", "claude", "versions")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(verDir, "2.1.183")
	if err := os.WriteFile(target, []byte("#!/bin/true\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(binDir, "claude")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	dirs := resolveToolDirs(link)
	if !slices.Contains(dirs, binDir) {
		t.Errorf("expected the launcher dir %q in %v", binDir, dirs)
	}
	if !slices.Contains(dirs, verDir) {
		t.Errorf("expected the symlink target dir %q in %v", verDir, dirs)
	}
}

func TestResolveToolDirs_IncludesNodeModulesRoot(t *testing.T) {
	// Codex's npm launcher lives under bin/codex and resolves to
	// lib/node_modules/@openai/codex/bin/codex.js. The CLI then loads its
	// platform optional dependency from a sibling package such as
	// @openai/codex-linux-x64, so binding only the script dir is not enough.
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	nodeModules := filepath.Join(root, "lib", "node_modules")
	codexBinDir := filepath.Join(nodeModules, "@openai", "codex", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(codexBinDir, "codex.js")
	if err := os.WriteFile(target, []byte("#!/usr/bin/env node\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(binDir, "codex")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	dirs := resolveToolDirs(link)
	if !slices.Contains(dirs, nodeModules) {
		t.Errorf("expected node_modules root %q in %v", nodeModules, dirs)
	}
}

func TestResolveToolDirs_BindsIntermediateSymlinkHops(t *testing.T) {
	// pnpm's global node: bin/node points into global/<id>/node_modules/node,
	// and that package dir is itself a link into the content store. Without
	// the middle directory in the jail, execvp fails with ENOENT on bin/node.
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	globalModules := filepath.Join(root, "global", "v11", "abc", "node_modules")
	storeBin := filepath.Join(root, "store", "node", "24", "node_modules", "node", "bin")
	for _, d := range []string{binDir, globalModules, storeBin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(storeBin, "node"), []byte("#!/bin/true\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../../../store/node/24/node_modules/node", filepath.Join(globalModules, "node")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(binDir, "node")
	if err := os.Symlink(filepath.Join(globalModules, "node", "bin", "node"), link); err != nil {
		t.Fatal(err)
	}

	dirs := resolveToolDirs(link)
	for _, want := range []string{binDir, globalModules, storeBin} {
		if !slices.Contains(dirs, want) {
			t.Errorf("expected %q in %v", want, dirs)
		}
	}
}
