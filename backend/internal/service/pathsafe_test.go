package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePathAllowingMissing_MissingLeafKeepsResolvedParent(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	// "link/not-created-yet" has no inode, but "link" must still be
	// dereferenced to "real" before the tail is re-appended.
	got, err := ResolvePathAllowingMissing(filepath.Join(link, "not-created-yet"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(mustEval(t, real), "not-created-yet")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolvePathAllowingMissing_MultipleMissingSegments(t *testing.T) {
	root := t.TempDir()
	got, err := ResolvePathAllowingMissing(filepath.Join(root, "a", "b", "c"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(mustEval(t, root), "a", "b", "c")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A dangling symlink Lstats fine but cannot be dereferenced. Treating it as
// a plain missing leaf would let a write follow the link outside the jail,
// so resolution must fail instead.
func TestResolvePathAllowingMissing_DanglingSymlinkRejected(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "never-created")
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolvePathAllowingMissing(link); err == nil {
		t.Fatal("expected error for dangling symlink")
	}
}

// "file.txt/child" is as absent as a missing directory; the existing
// ancestor still resolves so containment can be judged.
func TestResolvePathAllowingMissing_ChildOfRegularFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolvePathAllowingMissing(filepath.Join(file, "child"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(mustEval(t, root), "file.txt", "child")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPathWithin_PrefixSiblingRejected(t *testing.T) {
	base := t.TempDir()
	alice := filepath.Join(base, "alice")
	alice2 := filepath.Join(base, "alice2")
	for _, d := range []string{alice, alice2} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, child := range []string{alice2, filepath.Join(alice2, "x")} {
		within, err := PathWithin(alice, child)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if within {
			t.Errorf("PathWithin(%q, %q) = true, want false", alice, child)
		}
	}
}

func TestPathWithin_SymlinkEscapeRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "daymug.db")
	if err := os.WriteFile(secret, []byte("tokens"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}

	within, err := PathWithin(root, link)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if within {
		t.Error("symlink pointing outside the root was reported as inside")
	}
}

func TestPathWithin_NestedPathAccepted(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	within, err := PathWithin(root, filepath.Join(nested, "c.txt"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !within {
		t.Error("nested path inside the root was reported as outside")
	}
}

func TestResolveWithin_SymlinkToExternalFileRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "config.yaml")
	if err := os.WriteFile(secret, []byte("api_key: x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveWithin(root, "./link"); !errors.Is(err, ErrPathEscapes) {
		t.Fatalf("expected ErrPathEscapes, got %v", err)
	}
}

// Writes are the dangerous half: the destination does not exist yet, so the
// escape hides in a symlinked parent directory.
func TestResolveWithin_MissingTargetUnderSymlinkedParentRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "uploads")); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveWithin(root, "uploads/new-file.txt"); !errors.Is(err, ErrPathEscapes) {
		t.Fatalf("expected ErrPathEscapes, got %v", err)
	}
}

func TestResolveWithin_DotDotPrefixedFilenameAllowed(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"..config", "..", "...."} {
		full := filepath.Join(root, name)
		if name == ".." {
			continue
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, name := range []string{"..config", "...."} {
		got, err := ResolveWithin(root, name)
		if err != nil {
			t.Fatalf("ResolveWithin(%q) unexpected error: %v", name, err)
		}
		if want := filepath.Join(root, name); got != want {
			t.Errorf("ResolveWithin(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestResolveWithin_TraversalAndAbsoluteRejected(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"..", "../etc/passwd", "a/../../etc/passwd"} {
		if _, err := ResolveWithin(root, rel); !errors.Is(err, ErrPathEscapes) {
			t.Errorf("ResolveWithin(%q) = %v, want ErrPathEscapes", rel, err)
		}
	}
	if _, err := ResolveWithin(root, "/etc/passwd"); err == nil {
		t.Error("expected absolute path to be rejected")
	}
}

func TestResolveFromWithin_AllowsParentPathInsideRoot(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "agent")
	if err := os.Mkdir(base, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveFromWithin(root, base, "../MEMORY.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := filepath.Join(root, "MEMORY.md"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveFromWithin_RejectsPathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "agent")
	if err := os.Mkdir(base, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveFromWithin(root, base, "../../secret"); !errors.Is(err, ErrPathEscapes) {
		t.Fatalf("expected ErrPathEscapes, got %v", err)
	}
}

// The returned path is the unresolved join so delete/rename keep operating
// on the name the caller asked for rather than on a link's target.
func TestResolveWithin_ReturnsUnresolvedJoin(t *testing.T) {
	root := t.TempDir()
	inner := filepath.Join(root, "inner")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inner, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveWithin(root, "link/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := filepath.Join(root, "link", "file.txt"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
