package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// installRoot is the canonical layout daymug installs itself into:
//
//	~/.daymug/daymug           - the binary
//	~/.daymug/config.yaml       - YAML config (auto-resolved at startup)
//	~/.daymug/data/database.db  - SQLite file (auto-resolved at startup)
//
// All three paths are derived from the binary's own location at runtime,
// so once the binary is in place there is no env var or YAML knob to
// keep in sync. The matching logic lives in config.DefaultConfigFilename
// + config.DefaultDBPath().
const installDirName = ".daymug"

// runInstallLayout copies the currently-running binary into
// ~/.daymug/daymug, creates the sibling data/ + users/ directories, and
// (when missing) scaffolds a default config.yaml. Returns the absolute
// paths of the installed binary and its config so the caller — currently
// cmdBootstrap — can move on to runInstallService without re-deriving them.
//
// Idempotent: re-running overwrites the binary and leaves any existing
// config.yaml untouched unless forceConfig is true.
func runInstallLayout(forceConfig bool) (execPath, configPath string) {
	src, err := os.Executable()
	if err != nil {
		fatalf("cannot determine current binary path: %v", err)
	}
	if resolved, resErr := filepath.EvalSymlinks(src); resErr == nil {
		src = resolved
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fatalf("cannot determine home directory: %v", err)
	}
	installDir := filepath.Join(home, installDirName)
	dataDir := filepath.Join(installDir, "data")
	homeRoot := filepath.Join(installDir, "users")
	dst := filepath.Join(installDir, "daymug")
	configPath = filepath.Join(installDir, "config.yaml")

	for _, dir := range []string{installDir, dataDir, homeRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fatalf("create %s: %v", dir, err)
		}
	}

	// Refuse a self-overwrite — copying a running file onto itself either
	// silently succeeds (no-op) or breaks the executable depending on the
	// filesystem. Make the operator notice.
	if src == dst {
		fmt.Printf("Binary is already at %s — nothing to copy.\n", dst)
	} else {
		if err := copyExecutable(src, dst); err != nil {
			fatalf("copy binary: %v", err)
		}
		fmt.Printf("Installed binary: %s\n", dst)
	}

	if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) || forceConfig {
		if err := os.WriteFile(configPath, []byte(renderInitConfig(homeRoot)), 0o644); err != nil {
			fatalf("write %s: %v", configPath, err)
		}
		fmt.Printf("Wrote starter config: %s\n", configPath)
	} else {
		fmt.Printf("Existing config kept: %s (pass --force-config to regenerate)\n", configPath)
	}
	return dst, configPath
}

// copyExecutable atomically replaces dst with the contents of src and
// preserves +x. Writes via a sibling temp file + rename so the live
// binary is never half-written if the disk fills up mid-copy.
func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(dst), "daymug.install.*")
	if err != nil {
		return fmt.Errorf("create staging file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("copy contents: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close staging file: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, dst, err)
	}
	return nil
}
