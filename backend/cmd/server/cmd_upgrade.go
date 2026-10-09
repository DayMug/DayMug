package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
)

// cmdUpgrade is the user-facing CLI entrypoint that runs the full upgrade
// from start to finish synchronously: fetch manifest, download, verify,
// swap, restart, watchdog. Useful for headless installs where the admin
// UI isn't reachable.
//
// The admin HTTP endpoint (handler.AdminHandler.UpgradeApply) follows the
// same flow but returns to the client before invoking systemctl, since
// the request connection drops as the running process is killed. The
// watchdog spawned in step 6 is what makes both flows safe to run.
func cmdUpgrade() {
	fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to YAML config (overrides DAYMUG_CONFIG env)")
	rollback := fs.Bool("rollback", false, "Restore <bin>.bak over the active binary and restart the service")
	targetVersion := fs.String("version", "", "Pin the upgrade to a specific release tag (e.g. v1.2.3). Defaults to the latest release.")
	_ = fs.Parse(os.Args[2:])

	// Rollback included: it swaps <bin>.bak in place just like an upgrade.
	if service.SelfUpgradeDisabled() {
		fatalf("%v", service.ErrSelfUpgradeDisabled)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatalf("load config: %v", err)
	}

	binPath, err := resolveBinPath()
	if err != nil {
		fatalf("resolve binary path: %v", err)
	}

	if *rollback {
		if *targetVersion != "" {
			fatalf("--version cannot be combined with --rollback")
		}
		runRollback(cfg, binPath)
		return
	}
	runUpgrade(cfg, binPath, *targetVersion)
}

func runUpgrade(cfg *config.Config, binPath, targetVersion string) {
	upgrader := service.NewUpgrader(&cfg.Upgrade, Version, cfg.ListenAddr())

	manifestURL := service.DefaultManifestURL
	if targetVersion != "" {
		pinned, err := service.VersionedManifestURL(manifestURL, targetVersion)
		if err != nil {
			fatalf("resolve target version: %v", err)
		}
		upgrader.ManifestURL = pinned
		manifestURL = pinned
	}

	fmt.Printf("Current version: %s\n", Version)
	fmt.Printf("Fetching manifest %s\n", manifestURL)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	manifest, entry, err := upgrader.Check(ctx)
	if err != nil {
		// Not a failure worth a non-zero exit: the release just doesn't
		// carry this platform. Name a version the operator can pin to,
		// since the per-tag manifests outlive the rolling pointer.
		if errors.Is(err, service.ErrNoPlatformBuild) {
			fmt.Printf("%s has no build for %s/%s.\n",
				manifest.Version, runtime.GOOS, runtime.GOARCH)
			fmt.Println("Retry with `daymug upgrade --version vX.Y.Z` against a release that includes it.")
			return
		}
		fatalf("check: %v", err)
	}
	if !upgrader.HasUpdate(manifest) {
		fmt.Println("Already up to date.")
		return
	}
	fmt.Printf("New version: %s\n", manifest.Version)

	dlCtx, dlCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer dlCancel()
	staged, err := upgrader.DownloadAndVerify(dlCtx, entry, binPath)
	if err != nil {
		fatalf("download: %v", err)
	}

	fmt.Printf("Validating %s against %s\n", cfg.Path(), manifest.Version)
	pfCtx, pfCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer pfCancel()
	if err := service.PreflightConfig(pfCtx, staged, cfg.Path()); err != nil {
		_ = os.Remove(staged)
		fatalf("preflight: %v", err)
	}

	bak, err := service.SwapInPlace(binPath, staged)
	if err != nil {
		_ = os.Remove(staged)
		fatalf("swap: %v", err)
	}

	_ = service.WriteStatus(binPath, service.UpgradeStatus{
		Phase:      service.PhaseValidating,
		OldVersion: Version,
		NewVersion: manifest.Version,
		StartedAt:  time.Now().UTC(),
	})

	if err := service.SpawnWatchdog(
		bak, binPath,
		upgrader.HealthURL(),
		Version, manifest.Version,
		cfg.Upgrade.Service, cfg.Upgrade.ServiceMode,
		cfg.Upgrade.HealthTimeout.Duration,
	); err != nil {
		fatalf("spawn watchdog: %v", err)
	}

	fmt.Printf("Upgraded to %s. Watchdog will validate post-restart and roll back on failure.\n", manifest.Version)
}

func runRollback(cfg *config.Config, binPath string) {
	if _, err := os.Stat(service.BackupPath(binPath)); err != nil {
		fatalf("no backup at %s", service.BackupPath(binPath))
	}
	if err := service.RestoreBackup(binPath); err != nil {
		fatalf("restore: %v", err)
	}
	out, err := service.RestartService(cfg.Upgrade.Service, cfg.Upgrade.ServiceMode)
	if err != nil {
		fatalf("restart service: %v\n%s", err, out)
	}
	prev, _ := service.ReadStatus(binPath)
	_ = service.WriteStatus(binPath, service.UpgradeStatus{
		Phase:      service.PhaseRolledBack,
		OldVersion: prev.NewVersion,
		NewVersion: prev.OldVersion,
		StartedAt:  prev.StartedAt,
		FinishedAt: time.Now().UTC(),
		Error:      "manual rollback",
	})
	fmt.Println("Rolled back to previous binary.")
}

// resolveBinPath returns the absolute path of the running binary, with
// symlinks resolved so we always operate on the real file (not the
// /usr/local/bin/foo symlink an installer might use).
func resolveBinPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// cmdUpgradeWatchdog is invoked by SpawnWatchdog from the OLD binary
// (still on disk at <bin>.bak). It runs after the parent dies so the
// parent can return immediately to the admin UI / CLI caller.
//
// The parent has already validated the config against the new release
// (PreflightConfig) and swapped the binary on disk. Steps:
//
//  1. Sleep briefly so the parent has time to flush its HTTP response.
//  2. systemctl stop <service>, so nothing writes the database while it is
//     copied (macOS cannot hold a KeepAlive job down; there the snapshot is
//     taken live, which VACUUM INTO keeps consistent).
//  3. Snapshot the database into data/backups/, keeping the newest
//     UpgradeDBBackupKeep. The new release may run migrations the old one
//     cannot read back, so no snapshot means no upgrade.
//  4. systemctl restart <service> — which executes the NEW binary at <bin>.
//  5. Poll <health_url> for up to <timeout>. Success = 200 within window.
//  6. On success: write status=ok and exit.
//  7. On any failure: copy <bin>.bak over <bin>, restart again, write
//     status=rolled_back, exit.
//
// We don't bother fetching /api/version to compare against new_version —
// a 200 from /api/health on the right port is sufficient signal that the
// new binary booted. If we wanted stricter validation we could add a
// /api/version endpoint and compare; deferred until needed.
func cmdUpgradeWatchdog() {
	fs := flag.NewFlagSet("upgrade-watchdog", flag.ExitOnError)
	binPath := fs.String("bin", "", "Path to the active binary")
	healthURL := fs.String("health-url", "", "URL to poll for /api/health")
	oldVersion := fs.String("old-version", "", "Pre-upgrade version (for status reporting)")
	newVersion := fs.String("new-version", "", "Target version (for status reporting)")
	svc := fs.String("service", "daymug", "systemd service name")
	mode := fs.String("service-mode", "user", "systemd mode: user or system")
	timeout := fs.Duration("timeout", 60*time.Second, "Max time to wait for /api/health to return 200")
	dbPath := fs.String("db", "", "SQLite database to snapshot before starting the new binary")
	_ = fs.Parse(os.Args[2:])

	if *binPath == "" || *healthURL == "" {
		fatalf("upgrade-watchdog: --bin and --health-url are required")
	}

	// 1. Brief sleep so the parent can flush + exit cleanly.
	time.Sleep(2 * time.Second)

	// 2. Stop the old server so the database is quiescent.
	if out, err := service.StopService(*svc, *mode); err != nil && !errors.Is(err, service.ErrStopUnsupported) {
		watchdogRollback(*binPath, *oldVersion, *newVersion, *svc, *mode,
			fmt.Sprintf("stop failed: %v: %s", err, out))
		return
	}

	// 3. Snapshot the database.
	if *dbPath != "" {
		bctx, bcancel := context.WithTimeout(context.Background(), 15*time.Minute)
		backup, err := service.BackupDatabaseForUpgrade(bctx, *dbPath, *oldVersion, time.Now())
		bcancel()
		if err != nil {
			watchdogRollback(*binPath, *oldVersion, *newVersion, *svc, *mode,
				fmt.Sprintf("database backup failed: %v", err))
			return
		}
		fmt.Printf("database backed up to %s\n", backup)
	}

	// 4. Start the service on the new binary.
	if out, err := service.RestartService(*svc, *mode); err != nil {
		watchdogRollback(*binPath, *oldVersion, *newVersion, *svc, *mode,
			fmt.Sprintf("restart failed: %v: %s", err, out))
		return
	}

	// 5. Probe health.
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := service.ProbeHealth(ctx, &http.Client{Timeout: 3 * time.Second}, *healthURL); err != nil {
		watchdogRollback(*binPath, *oldVersion, *newVersion, *svc, *mode, err.Error())
		return
	}

	// 6. Healthy.
	_ = service.WriteStatus(*binPath, service.UpgradeStatus{
		Phase:      service.PhaseOK,
		OldVersion: *oldVersion,
		NewVersion: *newVersion,
		FinishedAt: time.Now().UTC(),
	})
}

func watchdogRollback(binPath, oldVersion, newVersion, svc, mode, reason string) {
	finished := time.Now().UTC()
	if err := service.RestoreBackup(binPath); err != nil {
		_ = service.WriteStatus(binPath, service.UpgradeStatus{
			Phase:      service.PhaseFailed,
			OldVersion: oldVersion,
			NewVersion: newVersion,
			FinishedAt: finished,
			Error:      fmt.Sprintf("restore backup failed: %v (original failure: %s)", err, reason),
		})
		return
	}
	if out, err := service.RestartService(svc, mode); err != nil {
		_ = service.WriteStatus(binPath, service.UpgradeStatus{
			Phase:      service.PhaseFailed,
			OldVersion: oldVersion,
			NewVersion: newVersion,
			FinishedAt: finished,
			Error:      fmt.Sprintf("restart after rollback failed: %v: %s (original: %s)", err, out, reason),
		})
		return
	}
	_ = service.WriteStatus(binPath, service.UpgradeStatus{
		Phase:      service.PhaseRolledBack,
		OldVersion: oldVersion,
		NewVersion: newVersion,
		FinishedAt: finished,
		Error:      reason,
	})
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}
