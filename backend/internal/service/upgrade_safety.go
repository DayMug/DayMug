package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// UpgradeDBBackupKeep is how many pre-upgrade database snapshots survive.
// Each is a full copy of a database that grows by hundreds of MB, so older
// ones are pruned rather than left to fill the disk.
const UpgradeDBBackupKeep = 3

const (
	upgradeBackupDirName = "backups"
	upgradeBackupPrefix  = "database-"
	upgradeBackupExt     = ".db"
	upgradeBackupTmpExt  = ".tmp"
)

// ErrStopUnsupported means the service manager cannot hold the service down:
// launchd's KeepAlive respawns a killed job at once, so on macOS the watchdog
// snapshots the live database instead.
var ErrStopUnsupported = errors.New("stopping the service is not supported on this platform")

// PreflightConfig asks the staged release to validate the config file it is
// about to boot with. The release being installed is the one whose rules
// matter: a key the current binary accepts may be rejected by the new one,
// and finding that out after the swap costs a failed start and a rollback.
func PreflightConfig(ctx context.Context, stagedBin, configPath string) error {
	if configPath == "" {
		return nil
	}
	out, err := exec.CommandContext(ctx, stagedBin, "check-config", "--config", configPath).CombinedOutput() // #nosec G204 -- stagedBin is the checksum-verified download.
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("new release rejects %s: %s", configPath, msg)
}

// StopService stops the service so its database is quiescent before the
// pre-upgrade snapshot. Linux only; see ErrStopUnsupported.
func StopService(service, mode string) ([]byte, error) {
	if runtime.GOOS != "linux" {
		return nil, ErrStopUnsupported
	}
	name, args := stopCommand(service, mode)
	return exec.Command(name, args...).CombinedOutput() // #nosec G204 -- mode/service from validated config.
}

func stopCommand(service, mode string) (string, []string) {
	args := []string{}
	if mode == "user" {
		args = append(args, "--user")
	}
	return "systemctl", append(args, "stop", service)
}

// UpgradeBackupDir is where pre-upgrade snapshots of dbPath live.
func UpgradeBackupDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), upgradeBackupDirName)
}

var unsafeVersionChars = regexp.MustCompile(`[^0-9A-Za-z._-]+`)

// BackupDatabaseForUpgrade snapshots dbPath into UpgradeBackupDir, named
// after the version being replaced, then prunes all but the newest
// UpgradeDBBackupKeep snapshots. Returns the snapshot path, or "" when there
// is no database yet. Any error must abort the upgrade: the snapshot is the
// only way back from a migration the old binary cannot read.
func BackupDatabaseForUpgrade(ctx context.Context, dbPath, fromVersion string, now time.Time) (string, error) {
	info, err := os.Stat(dbPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("stat database: %w", err)
	}
	dir := UpgradeBackupDir(dbPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	need := info.Size()
	if wal, werr := os.Stat(dbPath + "-wal"); werr == nil {
		need += wal.Size()
	}
	if err := ensureFreeSpace(dir, need); err != nil {
		return "", err
	}

	version := unsafeVersionChars.ReplaceAllString(strings.TrimSpace(fromVersion), "_")
	if version == "" {
		version = "unknown"
	}
	name := fmt.Sprintf("%s%s-%s%s", upgradeBackupPrefix, version, now.UTC().Format("20060102T150405Z"), upgradeBackupExt)
	final := filepath.Join(dir, name)
	tmp := final + upgradeBackupTmpExt
	_ = os.Remove(tmp)
	if err := store.SnapshotDatabase(ctx, dbPath, tmp); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("chmod backup: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("finalize backup: %w", err)
	}
	if removed, err := PruneUpgradeBackups(dir, UpgradeDBBackupKeep); err != nil {
		// The new snapshot is in place; a failed prune only costs disk.
		log.Printf("[upgrade] prune database backups: %v", err)
	} else if len(removed) > 0 {
		log.Printf("[upgrade] pruned old database backups: %s", strings.Join(removed, ", "))
	}
	return final, nil
}

// PruneUpgradeBackups deletes all but the newest keep snapshots in dir, plus
// temp files an interrupted snapshot left behind. Only files this package
// names are touched — hand-made backups in the same directory survive.
func PruneUpgradeBackups(dir string, keep int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type snap struct {
		name string
		mod  time.Time
	}
	var snaps []snap
	var removed []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, upgradeBackupPrefix) {
			continue
		}
		if strings.HasSuffix(n, upgradeBackupExt+upgradeBackupTmpExt) {
			if err := os.Remove(filepath.Join(dir, n)); err == nil {
				removed = append(removed, n)
			}
			continue
		}
		if !strings.HasSuffix(n, upgradeBackupExt) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		snaps = append(snaps, snap{n, info.ModTime()})
	}
	sort.Slice(snaps, func(i, j int) bool {
		if !snaps[i].mod.Equal(snaps[j].mod) {
			return snaps[i].mod.After(snaps[j].mod)
		}
		return snaps[i].name > snaps[j].name
	})
	var firstErr error
	for i := keep; i < len(snaps); i++ {
		if err := os.Remove(filepath.Join(dir, snaps[i].name)); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed = append(removed, snaps[i].name)
	}
	return removed, firstErr
}

// upgradeBackupHeadroom keeps the snapshot from taking the last free bytes
// the restarted server needs for its WAL and logs.
const upgradeBackupHeadroom = 256 << 20

func ensureFreeSpace(dir string, need int64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return fmt.Errorf("statfs %s: %w", dir, err)
	}
	free := int64(st.Bavail) * int64(st.Bsize) //nolint:gosec,unconvert // block counts fit int64 on any real disk; field types differ per OS
	if free < need+upgradeBackupHeadroom {
		return fmt.Errorf("not enough disk space for database backup in %s: need %d MB, have %d MB",
			dir, (need+upgradeBackupHeadroom)>>20, free>>20)
	}
	return nil
}
