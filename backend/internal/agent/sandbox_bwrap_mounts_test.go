package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBwrapMountPlanSkipsCoveredSymlinkDestination(t *testing.T) {
	root := t.TempDir()
	etcDir := filepath.Join(root, "etc")
	resolverDir := filepath.Join(root, "run", "resolver")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(resolverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resolverFile := filepath.Join(resolverDir, "stub-resolv.conf")
	if err := os.WriteFile(resolverFile, []byte("nameserver 127.0.0.53\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolverLink := filepath.Join(etcDir, "resolv.conf")
	if err := os.Symlink(resolverFile, resolverLink); err != nil {
		t.Fatal(err)
	}

	plan := newBwrapMountPlan("bwrap")
	for _, path := range []string{etcDir, resolverDir, resolverLink} {
		if err := plan.addIdentity("--ro-bind-try", path, bwrapReadOnly); err != nil {
			t.Fatalf("addIdentity(%q): %v", path, err)
		}
	}
	if slices.Contains(plan.args, resolverLink) {
		t.Fatalf("symlink destination must not be emitted: %v", plan.args)
	}
	if got := strings.Count(strings.Join(plan.args, " "), "--ro-bind-try"); got != 2 {
		t.Fatalf("bind count = %d, want 2: %v", got, plan.args)
	}
}

func TestBwrapMountPlanSkipsOrdinaryCoveredDescendant(t *testing.T) {
	root := t.TempDir()
	toolDir := filepath.Join(root, "usr", "local", "bin")
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatal(err)
	}

	plan := newBwrapMountPlan("bwrap")
	if err := plan.addIdentity("--ro-bind-try", filepath.Join(root, "usr"), bwrapReadOnly); err != nil {
		t.Fatal(err)
	}
	if err := plan.addIdentity("--ro-bind-try", toolDir, bwrapReadOnly); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.args, toolDir) {
		t.Fatalf("covered tool directory must not be emitted: %v", plan.args)
	}
}

func TestBwrapMountPlanKeepsAccessOverride(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "read-only")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	plan := newBwrapMountPlan("bwrap")
	if err := plan.addIdentity("--bind", root, bwrapReadWrite); err != nil {
		t.Fatal(err)
	}
	if err := plan.addIdentity("--ro-bind-try", child, bwrapReadOnly); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plan.args, child) {
		t.Fatalf("read-only override under writable parent was dropped: %v", plan.args)
	}
}

func TestBwrapMountPlanRejectsSymlinkAccessOverride(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "writable-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	plan := newBwrapMountPlan("bwrap")
	if err := plan.addIdentity("--ro-bind-try", root, bwrapReadOnly); err != nil {
		t.Fatal(err)
	}
	err := plan.addIdentity("--bind-try", link, bwrapReadWrite)
	if err == nil || !strings.Contains(err.Error(), "configure the resolved path") {
		t.Fatalf("expected actionable symlink error, got %v", err)
	}
}

func TestBwrapSandboxSkipsConfigAndPerRunBindsCoveredByJail(t *testing.T) {
	jail := t.TempDir()
	configDir := filepath.Join(jail, "config")
	attachmentDir := filepath.Join(jail, "attachments")
	for _, dir := range []string{configDir, attachmentDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	s := bwrapSandbox{bwrapPath: "/usr/bin/bwrap"}
	out, _, err := s.Wrap([]string{"/opt/claude"}, jail, WrapOpts{
		JailRoot:  jail,
		ConfigDir: configDir,
		ExtraBinds: []BindMount{
			{Source: attachmentDir, Writable: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(out, configDir) || slices.Contains(out, attachmentDir) {
		t.Fatalf("jail descendants must not be mounted twice: %v", out)
	}
}

func TestResolverTargetBind(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "run", "resolver.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("nameserver 127.0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "resolv.conf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if got := resolverTargetBind(link); got != target {
		t.Fatalf("resolverTargetBind = %q, want %q", got, target)
	}
	if got := resolverTargetBind(target); got != "" {
		t.Fatalf("regular resolver file returned extra bind %q", got)
	}
}
