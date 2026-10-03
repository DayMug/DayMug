package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/config"
)

// bwrapSandbox confines a jailed agent invocation to its work_dir using
// bubblewrap (bwrap). The system tree is bind-mounted read-only, the user's
// JailRoot read-write, and everything else (other users' work dirs, the
// central DayMug DB, /home/<server-user> at large, host secrets) is simply
// never mapped into the namespace, so it does not exist from the agent's
// point of view. Users whose sandbox_mode is "unrestricted" skip the wrapper
// entirely (Wrap returns argv unchanged).
//
// Caveat: ConfigDir (CLAUDE_CONFIG_DIR / CODEX_HOME) is shared across all
// users of one provider account and must be bound read-write so the CLI can
// read its credentials and write the session log. Per-user isolation of that
// directory needs a per-user config dir upstream; until then a jailed agent
// can still reach that one directory. The primary cross-user / host-secret
// exposure (arbitrary paths, the DB, /etc, other work dirs) is closed.
type bwrapSandbox struct {
	bwrapPath string
	cfg       config.SandboxConfig
}

// systemROBinds are the host paths every jailed agent gets read-only so the
// interpreter, shared libraries, and CLI tooling resolve. Missing entries are
// skipped (--ro-bind-try), so the list is safe across distros.
var systemROBinds = []string{
	"/usr", "/bin", "/sbin", "/lib", "/lib64", "/lib32",
	"/etc", "/opt",
}

// newBwrapSandbox resolves the bwrap binary up front so an operator who turns
// the sandbox on without bwrap installed gets a clear startup error rather
// than a per-turn failure.
func newBwrapSandbox(cfg *config.SandboxConfig) (Sandbox, error) {
	path, err := exec.LookPath(config.SandboxBwrap)
	if err != nil {
		return nil, fmt.Errorf("sandbox type %q requires the bwrap binary on PATH: %w", config.SandboxBwrap, err)
	}
	return bwrapSandbox{bwrapPath: path, cfg: *cfg}, nil
}

func (s bwrapSandbox) Wrap(argv []string, workDir string, opts WrapOpts) ([]string, []string, error) {
	if opts.Unrestricted {
		return argv, nil, nil
	}
	if len(argv) == 0 {
		return nil, nil, fmt.Errorf("bwrap sandbox: empty argv")
	}
	jail := opts.JailRoot
	if jail == "" {
		jail = workDir
	}
	if jail == "" {
		return nil, nil, fmt.Errorf("bwrap sandbox: jailed invocation has no work directory to confine to")
	}

	plan := newBwrapMountPlan(s.bwrapPath)
	for _, p := range systemROBinds {
		if err := plan.addIdentity("--ro-bind-try", p, bwrapReadOnly); err != nil {
			return nil, nil, fmt.Errorf("bwrap sandbox: %w", err)
		}
	}
	// /etc is mounted as a tree, so mounting /etc/resolv.conf again would
	// target the symlink inherited from the host and fail on current bwrap.
	// Mount only its resolved referent; this also supports NetworkManager and
	// other layouts instead of assuming systemd-resolved's /run path.
	if target := resolverTargetBind("/etc/resolv.conf"); target != "" {
		if err := plan.addIdentity("--ro-bind-try", target, bwrapReadOnly); err != nil {
			return nil, nil, fmt.Errorf("bwrap sandbox: resolver target: %w", err)
		}
	}
	// The agent binary (and its interpreter, e.g. node) may live outside the
	// system dirs — under nvm, ~/.local, /usr/local. Bind the binary's own
	// directory and node's, best-effort, so the launcher resolves.
	for _, dir := range resolveToolDirs(argv[0]) {
		if err := plan.addIdentity("--ro-bind-try", dir, bwrapReadOnly); err != nil {
			return nil, nil, fmt.Errorf("bwrap sandbox: tool directory: %w", err)
		}
	}
	// Best-effort: a PATH directory that cannot be mounted coherently only
	// costs the commands inside it, never the turn.
	for _, dir := range pathToolDirs(os.Getenv("PATH")) {
		if pathBindAllowed(dir, jail, opts.ConfigDir) {
			_ = plan.addIdentity("--ro-bind-try", dir, bwrapReadOnly)
		}
	}
	plan.args = append(plan.args,
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
	)
	if err := plan.addIdentity("--bind", jail, bwrapReadWrite); err != nil {
		return nil, nil, fmt.Errorf("bwrap sandbox: jail root: %w", err)
	}
	if opts.ConfigDir != "" {
		if err := plan.addIdentity("--bind", opts.ConfigDir, bwrapReadWrite); err != nil {
			return nil, nil, fmt.Errorf("bwrap sandbox: config directory: %w", err)
		}
	}
	for _, p := range s.cfg.ExtraROBinds {
		if err := plan.addIdentity("--ro-bind-try", p, bwrapReadOnly); err != nil {
			return nil, nil, fmt.Errorf("bwrap sandbox: extra read-only bind: %w", err)
		}
	}
	for _, p := range s.cfg.ExtraRWBinds {
		if err := plan.addIdentity("--bind-try", p, bwrapReadWrite); err != nil {
			return nil, nil, fmt.Errorf("bwrap sandbox: extra read-write bind: %w", err)
		}
	}
	for _, b := range opts.ExtraBinds {
		flag := "--ro-bind-try"
		if b.Writable {
			flag = "--bind-try"
		}
		target := b.Target
		if target == "" {
			target = b.Source
		}
		access := bwrapReadOnly
		if b.Writable {
			access = bwrapReadWrite
		}
		if err := plan.addBind(flag, b.Source, target, access); err != nil {
			return nil, nil, fmt.Errorf("bwrap sandbox: per-run bind: %w", err)
		}
	}
	plan.args = append(plan.args,
		"--chdir", workDir,
		"--unshare-all",
		"--die-with-parent",
		"--setenv", "HOME", jail,
	)
	if s.cfg.Network {
		// Re-share the host network namespace that --unshare-all dropped.
		plan.args = append(plan.args, "--share-net")
	}
	plan.args = append(plan.args, "--")
	plan.args = append(plan.args, argv...)
	return plan.args, nil, nil
}

// resolveToolDirs returns the directories that must be visible for the agent
// launcher to run: the binary's own directory plus node's (the CLIs are node
// programs). Both the on-PATH path AND its symlink-resolved target are added —
// installers (claude's own, nvm's node) put a launcher symlink on PATH that
// points at a versioned binary in a different directory, and binding only the
// symlink's directory leaves execvp chasing a target that doesn't exist inside
// the jail (the ENOENT "No such file or directory" failure). Duplicates and
// unresolved tools are dropped. Best-effort — the system binds usually already
// cover these.
func resolveToolDirs(binary string) []string {
	set := newToolDirSet()
	set.addExecutable(binary)
	if node, err := exec.LookPath("node"); err == nil {
		set.addExecutable(node)
	}
	return set.dirs
}

// symlinkHops lists every symlink, file or directory component, that
// resolving path passes through, each named by its fully resolved parent plus
// its own name. Binding each one's parent directory makes the whole chain
// resolvable; binding only the start and the final target leaves execvp
// stranded on the first link whose referent is not mounted.
func symlinkHops(path string) []string {
	if !filepath.IsAbs(path) {
		return nil
	}
	var links []string
	cur := "/"
	rest := splitPath(path)
	for len(rest) > 0 {
		name := rest[0]
		rest = rest[1:]
		if name == ".." {
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, name)
		target, err := os.Readlink(next)
		if err != nil {
			cur = next
			continue
		}
		// Same bound as the kernel's ELOOP limit on nested links.
		if len(links) >= 40 {
			return links
		}
		links = append(links, next)
		if !filepath.IsAbs(target) {
			target = filepath.Join(cur, target)
		}
		rest = append(splitPath(target), rest...)
		cur = "/"
	}
	return links
}

func splitPath(path string) []string {
	var parts []string
	for _, p := range strings.Split(filepath.Clean(path), "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	return parts
}
