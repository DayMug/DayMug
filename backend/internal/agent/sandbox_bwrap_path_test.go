package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
)

func writeExec(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func assertDirs(t *testing.T, dirs []string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !slices.Contains(dirs, w) {
			t.Errorf("expected %q in %v", w, dirs)
		}
	}
}

func TestPathToolDirs_FollowsLinkOutOfPathDir(t *testing.T) {
	// corepack's yarn on ~/.local/bin points into nvm's node_modules; without
	// that tree the PATH entry is a dangling link inside the jail.
	root := t.TempDir()
	bin := filepath.Join(root, "local", "bin")
	nodeModules := filepath.Join(root, "nvm", "lib", "node_modules")
	target := filepath.Join(nodeModules, "corepack", "dist", "yarn.js")
	writeExec(t, target, "#!/usr/bin/env node\n")
	symlink(t, target, filepath.Join(bin, "yarn"))

	assertDirs(t, pathToolDirs(bin), bin, filepath.Dir(target), nodeModules)
}

func TestPathToolDirs_FollowsCmdShimBasedir(t *testing.T) {
	// pnpm's bin/pnpm is a script, not a link: the real program is only named
	// by its exec line, relative to the script's own directory.
	root := t.TempDir()
	bin := filepath.Join(root, "pnpm", "bin")
	exe := filepath.Join(root, "pnpm", "global", "v11", "abc", "node_modules", "@pnpm", "exe", "pnpm")
	writeExec(t, exe, "\x7fELF")
	writeExec(t, filepath.Join(bin, "pnpm"), "#!/bin/sh\n"+
		`basedir=$(dirname "$(echo "$0" | sed -e 's,\\,/,g')")`+"\n"+
		`exec "$basedir/../global/v11/abc/node_modules/@pnpm/exe/pnpm"   "$@"`+"\n")

	assertDirs(t, pathToolDirs(bin), bin, filepath.Dir(exe))
}

func TestPathToolDirs_FollowsAbsoluteExecIntoVirtualenv(t *testing.T) {
	// A wrapper on PATH execs a tool inside a virtualenv; python only finds the
	// venv's site-packages when the prefix holding pyvenv.cfg is mounted.
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	venv := filepath.Join(root, "app", "venv")
	python := filepath.Join(venv, "bin", "python3")
	writeExec(t, python, "\x7fELF")
	writeExec(t, filepath.Join(venv, "bin", "app"), "#!"+python+"\nimport app\n")
	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(bin, "app"), "#!/usr/bin/env bash\nexec \""+filepath.Join(venv, "bin", "app")+"\" \"$@\"\n")

	assertDirs(t, pathToolDirs(bin), bin, venv)
}

func TestPathToolDirs_NoSitePackagesOutsideVirtualenv(t *testing.T) {
	// pip --user packages resolve under $HOME, which the jail replaces, so
	// mounting the host's ~/.local/lib would expose it without making it work.
	root := t.TempDir()
	prefix := filepath.Join(root, "local")
	if err := os.MkdirAll(filepath.Join(prefix, "lib", "python3.12"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeExec(t, filepath.Join(prefix, "bin", "tabulate"), "#!/usr/bin/python3\nfrom tabulate import _main\n")

	dirs := pathToolDirs(filepath.Join(prefix, "bin"))
	for _, d := range dirs {
		if strings.HasPrefix(d, filepath.Join(prefix, "lib")) || d == prefix {
			t.Errorf("unexpected python prefix bind %q in %v", d, dirs)
		}
	}
}

func TestPathToolDirs_SkipsSystemAndRelativeEntries(t *testing.T) {
	if dirs := pathToolDirs("/usr/bin:/bin:relative/bin"); len(dirs) != 0 {
		t.Errorf("system and relative PATH entries need no extra binds, got %v", dirs)
	}
}

func TestPathBindAllowed(t *testing.T) {
	cases := []struct {
		name string
		dir  string
		want bool
	}{
		{"tool dir", "/opt/tools/bin", true},
		{"ancestor of jail", "/srv/daymug", false},
		{"inside jail", "/srv/daymug/agents/u1/bin", false},
		{"ancestor of config dir", "/srv/accounts", false},
		{"a home directory", "/home/someone", false},
		{"top-level directory", "/srv", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathBindAllowed(tc.dir, "/srv/daymug/agents/u1", "/srv/accounts/claude"); got != tc.want {
				t.Errorf("pathBindAllowed(%q) = %v, want %v", tc.dir, got, tc.want)
			}
		})
	}
}

func TestBwrapSandbox_BindsPathToolsReadOnly(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "tools", "bin")
	writeExec(t, filepath.Join(bin, "pnpm"), "\x7fELF")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin")
	jail := filepath.Join(root, "jail")

	s := bwrapSandbox{bwrapPath: "/usr/bin/bwrap", cfg: config.SandboxConfig{}}
	out, _, err := s.Wrap([]string{"/usr/bin/true"}, jail, WrapOpts{JailRoot: jail})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if !strings.Contains(strings.Join(out, " "), "--ro-bind-try "+bin+" "+bin) {
		t.Errorf("PATH tool dir %q not bound read-only: %v", bin, out)
	}
}
