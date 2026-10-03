package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type bwrapBindAccess uint8

const (
	bwrapReadOnly bwrapBindAccess = iota
	bwrapReadWrite
)

type bwrapIdentityMount struct {
	path   string
	access bwrapBindAccess
}

// bwrapMountPlan keeps identity bind mounts coherent as the namespace is
// assembled. Once a parent is mounted, its descendants already exist at the
// destination. Re-mounting one of those descendants with the same access is
// redundant, and bwrap rejects it when that destination happens to be a
// symlink (the common /etc/resolv.conf layout on systemd hosts).
type bwrapMountPlan struct {
	args     []string
	identity []bwrapIdentityMount
}

func newBwrapMountPlan(binary string) *bwrapMountPlan {
	return &bwrapMountPlan{args: []string{binary}}
}

func (p *bwrapMountPlan) addIdentity(flag, path string, access bwrapBindAccess) error {
	clean := filepath.Clean(path)
	if mounted, ok := p.coveringIdentity(clean); ok {
		resolved, err := filepath.EvalSymlinks(clean)
		if err != nil {
			if mounted.access == access && strings.HasSuffix(flag, "-try") && os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("resolve bind destination %q already visible through %q: %w", clean, mounted.path, err)
		}
		resolved = filepath.Clean(resolved)
		if resolved != clean {
			if target, covered := p.coveringIdentity(resolved); covered && target.access == access {
				return nil
			}
			return fmt.Errorf("bind destination %q is a symlink to %q outside an equivalent existing mount; configure the resolved path instead", clean, resolved)
		}
		if mounted.access == access {
			return nil
		}
	}

	p.args = append(p.args, flag, clean, clean)
	p.identity = append(p.identity, bwrapIdentityMount{path: clean, access: access})
	return nil
}

func (p *bwrapMountPlan) addBind(flag, source, target string, access bwrapBindAccess) error {
	if target == "" || filepath.Clean(source) == filepath.Clean(target) {
		return p.addIdentity(flag, source, access)
	}
	p.args = append(p.args, flag, source, target)
	return nil
}

func (p *bwrapMountPlan) coveringIdentity(path string) (bwrapIdentityMount, bool) {
	for i := len(p.identity) - 1; i >= 0; i-- {
		if pathWithinOrEqual(p.identity[i].path, path) {
			return p.identity[i], true
		}
	}
	return bwrapIdentityMount{}, false
}

func pathWithinOrEqual(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

// resolverTargetBind returns the symlink referent that must be mounted for
// the /etc/resolv.conf link inherited through the /etc bind to work. A regular
// resolv.conf is already covered by /etc and needs no separate mount.
func resolverTargetBind(path string) string {
	clean := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return ""
	}
	resolved = filepath.Clean(resolved)
	if resolved == clean {
		return ""
	}
	return resolved
}
