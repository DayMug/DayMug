# Releases and self-upgrade

## Release workflow

`.github/workflows/release.yml` runs whenever a `v*.*.*` tag is pushed, on a GitHub-hosted `ubuntu-latest` runner. It builds the frontend once, cross-compiles **linux/amd64**, **linux/arm64** and **darwin/arm64**, and publishes them as a GitHub Release together with `checksums.txt`, `manifest.json`, `install.sh` and `config.example.yaml`. GitHub keeps every release, so every version ever published stays installable.

Two release-policy defaults are deliberate:

- **Every platform, every release.** There is no per-tag platform selection.
- **Third digit only.** `v1.2.45 → v1.2.46`. Minor and major bumps are a deliberate maintainer decision, because a tag push moves the self-upgrade pointer.

Every run signs/notarizes the darwin/arm64 binary via `quill` when its secrets are set, and `manifest.json` carries every platform under `binaries`. There is no background auto-upgrade: installs only move when an admin clicks Update or runs `daymug upgrade`. Exact commands: [RELEASING.md](../../RELEASING.md).

## Missing platform builds

`Upgrader.Check` installs `binaries[GOOS/GOARCH]` from the manifest and nothing else. `ErrNoPlatformBuild` fires when that key is absent. Callers render it as "this release has nothing for your platform" rather than an error: the admin panel names the version it skipped, `daymug upgrade` prints the same and exits 0, and Apply refuses before draining conversations.

## Self-upgrade and watchdog rollback

The running server can self-upgrade. Admins click "Check for updates" in the Settings → Admin → Global panel; the backend fetches the manifest and verifies the SHA-256 of the matching binary. Before touching the installed binary it runs `<staged> check-config --config <loaded config>` so the incoming release validates the exact file the server booted with; a rejection — including from a release too old to have `check-config` — aborts the upgrade and is reported in the upgrade status. It then swaps the binary atomically and spawns a detached watchdog that:

1. stops the service (`systemctl [--user] stop`), so the database is quiescent;
2. snapshots `data/database.db` with `VACUUM INTO` to `data/backups/database-<old version>-<UTC time>.db` (0600, after a free-space check) and prunes all but the newest 3 — hand-made files in that directory are never touched. macOS cannot hold a KeepAlive job down, so there the snapshot is taken live;
3. starts the new binary and probes the configured health path, which reads the database and answers 503 if it cannot.

A failed stop, a failed snapshot, or a failed probe restores `<bin>.bak` and restarts the old binary. To recover data after a bad migration, stop the service and copy the newest snapshot over `data/database.db`.

Nothing upgrades in the background: an install only moves when an admin clicks Update or runs `daymug upgrade`.

Setting `DAYMUG_DISABLE_SELF_UPGRADE=1` turns all of this off — check, apply, rollback and `daymug upgrade` refuse with a message saying to upgrade by replacing the binary or pulling a new image. The container image sets it (see [guides/docker.md](../guides/docker.md)).

This works with the Linux systemd user unit and the macOS LaunchAgent (which starts after user login) because their process supervisors remain active. `--skip-service` direct-process installs do not provide the automatic restart contract. Manual rollback is available from the same panel or via `daymug upgrade --rollback`.

## Manifest

The manifest URL is `service.DefaultManifestURL` — `https://github.com/<repo>/releases/latest/download/manifest.json`, with `<repo>` set at build time by the release workflow's ldflags (not configurable at runtime). `daymug upgrade --version vX.Y.Z` rewrites it to the tag's own `releases/download/vX.Y.Z/manifest.json`. Downloads are bounded by a 30s wait for response headers rather than a whole-transfer timeout, since a slow link to GitHub's CDN can need minutes for a full binary. Only watchdog knobs (`upgrade.service`, `service_mode`, `health_path`, `health_timeout`) are exposed in YAML.
