package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ResolveAgentBinary returns the path to invoke for an agent CLI name
// (e.g. "claude" or "codex"). PATH lookup wins when it succeeds; otherwise
// we scan a few well-known npm-style install dirs so a user who installed
// the CLI via nvm / npm-global / pnpm doesn't have to hand-maintain the
// server process's PATH on top of their interactive shell.
//
// Results are cached per-name for the life of the process. Callers can
// short-circuit the lookup by passing a value that already looks like a
// path (absolute, or containing a path separator) — useful for tests and
// for operators who want to pin an explicit binary without touching PATH.
//
// If nothing is found, the bare name is returned so the eventual exec
// surfaces the familiar "executable file not found in $PATH" message
// instead of an empty argv[0].
func ResolveAgentBinary(name string) string {
	if name == "" {
		return name
	}
	if filepath.IsAbs(name) || strings.ContainsRune(name, os.PathSeparator) {
		return name
	}
	if v, ok := agentBinaryCache.Load(name); ok {
		cached := v.(string)
		if cached != name && executableFile(cached) {
			return cached
		}
		resolved := discoverAgentBinary(name)
		agentBinaryCache.Store(name, resolved)
		return resolved
	}
	onceAny, _ := agentBinaryOnce.LoadOrStore(name, &sync.Once{})
	once := onceAny.(*sync.Once)
	once.Do(func() {
		agentBinaryCache.Store(name, discoverAgentBinary(name))
	})
	v, _ := agentBinaryCache.Load(name)
	return v.(string)
}

var (
	agentBinaryOnce  sync.Map // name -> *sync.Once
	agentBinaryCache sync.Map // name -> string
)

// discoverAgentBinary is the uncached resolution. Exported as a package
// var via the agentBinarySearchDirs seam so tests can swap the scan list
// without poking the real $HOME.
func discoverAgentBinary(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range agentBinarySearchDirs() {
		candidate := filepath.Join(dir, name)
		if executableFile(candidate) {
			return candidate
		}
	}
	return name
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

// agentBinarySearchDirs lists directories scanned after $PATH misses.
// Order: nvm-managed node bins first (highest semver, with the nvm
// default alias promoted to the front when it points at an existing
// directory), then per-user npm/pnpm globals, then /usr/local/bin as a
// final catch. Indirected as a var so tests can replace the whole list.
var agentBinarySearchDirs = func() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dirs := nvmBinDirs(home)
	dirs = append(dirs,
		filepath.Join(home, ".npm-global", "bin"),
		filepath.Join(home, ".local", "share", "pnpm"),
		filepath.Join(home, ".local", "bin"),
		"/usr/local/bin",
	)
	return dirs
}

// nvmBinDirs walks ~/.nvm/versions/node/* and returns the per-version
// bin dirs in resolution order. The nvm `alias/default` value (when it
// names an existing version) wins; remaining versions are sorted by
// parsed semver descending so a freshly-installed node beats a stale
// older one.
func nvmBinDirs(home string) []string {
	root := filepath.Join(home, ".nvm", "versions", "node")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	versions := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			versions = append(versions, e.Name())
		}
	}
	if len(versions) == 0 {
		return nil
	}
	sort.SliceStable(versions, func(i, j int) bool {
		return compareNvmVersion(versions[i], versions[j]) > 0
	})
	if def := resolveNvmDefault(home, versions); def != "" {
		for i, v := range versions {
			if v == def {
				versions = append([]string{v}, append(versions[:i:i], versions[i+1:]...)...)
				break
			}
		}
	}
	dirs := make([]string, 0, len(versions))
	for _, v := range versions {
		dirs = append(dirs, filepath.Join(root, v, "bin"))
	}
	return dirs
}

// resolveNvmDefault reads ~/.nvm/alias/default and tries to map its
// contents to one of the installed versions. Returns "" when no match
// is found (e.g. the alias points at "lts/*" or a version that has been
// uninstalled), in which case the caller keeps the lex-semver order.
func resolveNvmDefault(home string, installed []string) string {
	raw, err := os.ReadFile(filepath.Join(home, ".nvm", "alias", "default"))
	if err != nil {
		return ""
	}
	want := strings.TrimSpace(string(raw))
	if want == "" {
		return ""
	}
	if !strings.HasPrefix(want, "v") {
		want = "v" + want
	}
	for _, v := range installed {
		if v == want {
			return v
		}
	}
	for _, v := range installed {
		if strings.HasPrefix(v, want+".") {
			return v
		}
	}
	return ""
}

// compareNvmVersion compares two "vMAJOR.MINOR.PATCH"-style strings
// numerically. Pre-release suffixes are ignored (the numeric prefix wins).
// Unparseable segments fall back to lexicographic comparison so the sort
// is total.
func compareNvmVersion(a, b string) int {
	pa, oka := parseNvmVersion(a)
	pb, okb := parseNvmVersion(b)
	if !oka || !okb {
		return strings.Compare(a, b)
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var ai, bi int
		if i < len(pa) {
			ai = pa[i]
		}
		if i < len(pb) {
			bi = pb[i]
		}
		if ai != bi {
			if ai > bi {
				return 1
			}
			return -1
		}
	}
	return 0
}

func parseNvmVersion(name string) ([]int, bool) {
	s := strings.TrimPrefix(name, "v")
	// Drop semver pre-release / build metadata in one shot so the trailing
	// "1" in "v20.0.0-rc.1" doesn't sneak in as an extra version component.
	if dash := strings.IndexByte(s, '-'); dash >= 0 {
		s = s[:dash]
	}
	if s == "" {
		return nil, false
	}
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, len(out) > 0
}
