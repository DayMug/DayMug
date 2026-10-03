package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrPathEscapes reports a path that resolves outside the root it was
// supposed to stay under. Callers translate it into their own 403/400.
var ErrPathEscapes = errors.New("path escapes the root directory")

// ResolvePathAllowingMissing dereferences every symlink in path and returns
// the canonical absolute form.
//
// Unlike filepath.EvalSymlinks it tolerates a target that does not exist
// yet, which is the normal case on a write path: the file being created has
// no inode, but every directory leading to it must still be dereferenced
// before a containment check can be trusted. The deepest ancestor that
// actually exists is resolved and the missing tail is re-appended verbatim.
//
// "Missing" is decided with Lstat, not with EvalSymlinks' error: a dangling
// symlink Lstats fine but fails to resolve, and treating it as a missing
// leaf would let `workdir/link -> /etc/newfile` pass the containment test and
// then have the write follow the link outside the jail. Such a path is
// rejected instead.
func ResolvePathAllowingMissing(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	var missing []string
	cur := abs
	for {
		if _, lerr := os.Lstat(cur); lerr == nil {
			break
		} else if !isMissingPathErr(lerr) {
			return "", lerr
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Walked past the filesystem root without finding anything
			// that exists — nothing left to dereference against.
			return "", fmt.Errorf("resolve %q: no existing ancestor", abs)
		}
		missing = append([]string{filepath.Base(cur)}, missing...)
		cur = parent
	}

	resolved, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	if len(missing) == 0 {
		return resolved, nil
	}
	return filepath.Join(append([]string{resolved}, missing...)...), nil
}

// isMissingPathErr reports whether err means "this path has no inode".
// ENOTDIR joins ENOENT because a path like "file.txt/child" is just as
// absent, and its existing ancestors are still worth resolving.
func isMissingPathErr(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// PathWithin reports whether target, with all symlinks resolved on both
// sides, is root itself or a descendant of it.
//
// Containment is decided with filepath.Rel rather than a string prefix so
// "/home/alice2" is not mistaken for a child of "/home/alice". A non-nil
// error means the answer is unknown (broken symlink, permission denied,
// symlink loop); callers must treat that as "not within".
func PathWithin(root, target string) (bool, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(target) == "" {
		return false, errors.New("empty path")
	}
	resolvedRoot, err := ResolvePathAllowingMissing(root)
	if err != nil {
		return false, fmt.Errorf("resolve root: %w", err)
	}
	resolvedTarget, err := ResolvePathAllowingMissing(target)
	if err != nil {
		return false, fmt.Errorf("resolve target: %w", err)
	}
	return relIsWithin(resolvedRoot, resolvedTarget)
}

// relIsWithin is the separator-aware containment test on two already
// canonical absolute paths.
func relIsWithin(root, target string) (bool, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false, err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false, nil
	}
	return true, nil
}

// ResolveWithin joins relPath under root and returns the joined absolute
// path, having verified that its symlink-resolved form stays inside root.
//
// The returned path is the *unresolved* join, so callers keep operating on
// the name the user asked for — deleting or renaming a symlink still touches
// the link rather than its target. Only the security decision uses the
// resolved form.
//
// relPath must be relative. Traversal and symlink checks are both enforced
// against root after the path is joined.
func ResolveWithin(root, relPath string) (string, error) {
	return ResolveFromWithin(root, root, relPath)
}

// ResolveFromWithin joins relPath under base and returns the joined absolute
// path after verifying that its symlink-resolved form stays inside root.
//
// base may be a descendant of root. This is useful when paths are presented
// relative to a narrower workspace while the authorization boundary is a
// containing directory. A leading ".." segment is therefore allowed only
// when the final resolved target remains inside root.
func ResolveFromWithin(root, base, relPath string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("empty root directory")
	}
	if strings.TrimSpace(base) == "" {
		return "", errors.New("empty base directory")
	}
	if relPath == "" {
		relPath = "."
	}
	cleaned := filepath.Clean(relPath)
	if filepath.IsAbs(cleaned) {
		return "", errors.New("absolute paths not allowed")
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	full := filepath.Join(absBase, cleaned)

	within, err := PathWithin(absRoot, full)
	if err != nil {
		// Only ErrPathEscapes is wrapped, and deliberately so: this is the
		// sandbox boundary, and the resolver error underneath carries host
		// absolute paths inside *fs.PathError. Callers (the file handler) branch on the sentinel and echo the rest to users, so the
		// inner error stays a flattened string that cannot be errors.As'd back
		// into a structured path leak.
		//nolint:errorlint // see above: the second error is intentionally not wrapped
		return "", fmt.Errorf("%w: %s", ErrPathEscapes, err)
	}
	if !within {
		return "", ErrPathEscapes
	}
	return full, nil
}
