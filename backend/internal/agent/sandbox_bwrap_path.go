package agent

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Bounds that keep a per-turn PATH scan cheap on hosts with large tool dirs.
const (
	pathDirEntryLimit = 4096
	shimReadLimit     = 64 << 10
	shimNestingLimit  = 2
)

// shimPathRef matches the paths a launcher script hands to exec: absolute ones
// and cmd-shim's "$basedir/..." form (npm, pnpm), which is relative to the
// directory the script was invoked from.
var shimPathRef = regexp.MustCompile(`(\$\{?basedir\}?)?(/[^\s"'` + "`" + `$;|&<>()]+)`)

// toolDirSet collects, deduplicated and in discovery order, the host
// directories a jailed process needs to execute some set of tools.
type toolDirSet struct {
	seen    map[string]bool
	scanned map[string]bool
	dirs    []string
}

func newToolDirSet() *toolDirSet {
	return &toolDirSet{seen: map[string]bool{}, scanned: map[string]bool{}}
}

func (s *toolDirSet) addDir(dir string) {
	if dir == "" || dir == "." || dir == "/" || s.seen[dir] {
		return
	}
	s.seen[dir] = true
	s.dirs = append(s.dirs, dir)
}

// addNodeModulesRoot binds the enclosing node_modules of a package file: npm
// CLIs load sibling packages (codex's platform binary, for one) from there.
func (s *toolDirSet) addNodeModulesRoot(path string) {
	for dir := filepath.Dir(filepath.Clean(path)); dir != "." && dir != "/" && dir != ""; dir = filepath.Dir(dir) {
		if filepath.Base(dir) == "node_modules" {
			s.addDir(dir)
			return
		}
	}
}

// addExecutable makes path executable inside the jail: its own directory,
// every symlink hop on the way to the real file, and that file's directory.
func (s *toolDirSet) addExecutable(path string) {
	s.addDir(filepath.Dir(path))
	s.addNodeModulesRoot(path)
	// A chain like pnpm's bin/node -> global/<id>/node_modules/node/bin/node,
	// where node_modules/node is itself a link into the store, is only
	// walkable inside the jail if every intermediate link is visible too.
	for _, link := range symlinkHops(path) {
		s.addDir(filepath.Dir(link))
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		s.addDir(filepath.Dir(real))
		s.addNodeModulesRoot(real)
	}
}

// addCommand handles one entry of a PATH directory. Beyond the executable
// itself, launcher scripts reach their real program through paths no symlink
// reveals — pnpm's `exec "$basedir/../global/<id>/.../pnpm"`, a wrapper that
// execs into a virtualenv — so the script text is scanned for those too.
func (s *toolDirSet) addCommand(path string, depth int) {
	s.addExecutable(path)
	real, err := filepath.EvalSymlinks(path)
	if err != nil || s.scanned[real] || depth > shimNestingLimit {
		return
	}
	s.scanned[real] = true
	head := readScriptHead(real)
	if head == nil {
		return
	}
	interpreter, name := shebangInterpreter(head)
	if interpreter != "" {
		s.addExecutable(interpreter)
	}
	if strings.HasPrefix(name, "python") {
		s.addPythonPrefix(path)
		if interpreter != "" {
			s.addPythonPrefix(interpreter)
		}
	}
	invokedFrom := filepath.Dir(path)
	for _, m := range shimPathRef.FindAllSubmatch(head, -1) {
		ref := string(m[2])
		if len(m[1]) > 0 {
			ref = filepath.Join(invokedFrom, ref)
		}
		if isExecutableFile(ref) {
			s.addCommand(ref, depth+1)
		}
	}
}

// addPythonPrefix exposes a virtualenv's whole prefix: python only treats it
// as a venv, and so only finds its site-packages, when pyvenv.cfg is visible
// next to bin/. `pip install --user` packages are deliberately not handled —
// python locates those under $HOME, which inside the jail is the jail itself.
func (s *toolDirSet) addPythonPrefix(executable string) {
	prefix := filepath.Dir(filepath.Dir(executable))
	if prefix == "/" || prefix == "." {
		return
	}
	if _, err := os.Stat(filepath.Join(prefix, "pyvenv.cfg")); err == nil {
		s.addDir(prefix)
	}
}

// pathToolDirs returns the host directories a jailed shell needs so that the
// commands on pathEnv outside the system binds (nvm, pnpm, bun, cargo,
// ~/.local/bin, virtualenvs, …) run the same as they do on the host. Without
// it a command's availability depends on whether it happens to share an
// install directory with the agent binary.
func pathToolDirs(pathEnv string) []string {
	set := newToolDirSet()
	for _, dir := range filepath.SplitList(pathEnv) {
		if !filepath.IsAbs(dir) || underSystemBind(dir) {
			continue
		}
		dir = filepath.Clean(dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		// fnm and similar managers put a symlinked directory on PATH.
		for _, link := range symlinkHops(dir) {
			set.addDir(filepath.Dir(link))
		}
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			set.addDir(real)
		}
		set.addDir(dir)
		if len(entries) > pathDirEntryLimit {
			entries = entries[:pathDirEntryLimit]
		}
		for _, e := range entries {
			set.addCommand(filepath.Join(dir, e.Name()), 0)
		}
	}
	return set.dirs
}

// shebangInterpreter returns the absolute interpreter path of a "#!" line (empty
// for `#!/usr/bin/env x`, which resolves through PATH like any command) and the
// interpreter's program name.
func shebangInterpreter(head []byte) (path, name string) {
	firstLine, _, _ := bytes.Cut(head, []byte("\n"))
	fields := strings.Fields(strings.TrimPrefix(string(firstLine), "#!"))
	if len(fields) == 0 {
		return "", ""
	}
	if filepath.Base(fields[0]) == "env" {
		for _, f := range fields[1:] {
			if !strings.HasPrefix(f, "-") {
				return "", filepath.Base(f)
			}
		}
		return "", ""
	}
	if !filepath.IsAbs(fields[0]) {
		return "", ""
	}
	return fields[0], filepath.Base(fields[0])
}

func underSystemBind(path string) bool {
	for _, sys := range systemROBinds {
		if pathWithinOrEqual(sys, path) {
			return true
		}
	}
	return false
}

func isExecutableFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// readScriptHead returns the first bytes of an executable "#!" script, or nil
// for binaries and anything unreadable.
func readScriptHead(path string) []byte {
	if !isExecutableFile(path) {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	head, err := io.ReadAll(io.LimitReader(f, shimReadLimit))
	if err != nil || !bytes.HasPrefix(head, []byte("#!")) {
		return nil
	}
	return head
}

// pathBindAllowed rejects PATH-derived directories that would undo the jail:
// a home directory or any ancestor of a protected path (the jail, the shared
// credential dir) exposes other users' work and the server's own state, and a
// directory inside one would be shadowed by its read-write bind anyway.
func pathBindAllowed(dir string, protected ...string) bool {
	for _, p := range protected {
		if p != "" && (pathWithinOrEqual(dir, p) || pathWithinOrEqual(p, dir)) {
			return false
		}
	}
	if parent := filepath.Dir(dir); parent == "/home" || parent == "/" {
		return false
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == dir {
		return false
	}
	return true
}
