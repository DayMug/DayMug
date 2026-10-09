// Package service - upgrader.go implements self-upgrade against a
// manifest URL (a GitHub Release asset published by the release pipeline).
//
// The flow has three actors:
//
//  1. The running server. Receives an admin "apply" request, downloads the
//     new binary, verifies its sha256, copies the running binary aside as
//     <bin>.bak, then writes the new binary at <bin>. Spawns a detached
//     watchdog using <bin>.bak (so the watchdog continues running even
//     after this process is killed by the service-manager restart) and
//     returns 200 to the client.
//
//  2. The watchdog (`daymug upgrade-watchdog ...` invoked from <bin>.bak).
//     Sleeps briefly, restarts the service via `systemctl` (Linux) or
//     `launchctl kickstart -k` (macOS), then polls health_url for up to
//     health_timeout. If the new process comes up healthy AND reports the
//     new version, the watchdog deletes the pending-marker and exits. If
//     polling fails, it restores <bin>.bak over <bin>, restarts the
//     service again, and writes a "rolled_back" marker so the admin UI
//     can surface the failure.
//
//  3. The admin UI. Polls /api/admin/upgrade/status while the watchdog
//     runs and renders one of: ok / validating / rolled_back / failed.
//
// Tests cover manifest parse + version diff, sha256 verification, and the
// atomic swap roundtrip. The watchdog itself is exercised end-to-end by
// the CLI integration test in cmd_upgrade_test.go.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DayMug/DayMug/backend/internal/config"
)

// Manifest is the JSON document at config.Upgrade.ManifestURL.
type Manifest struct {
	Version    string                   `json:"version"`
	ReleasedAt string                   `json:"released_at"`
	Binaries   map[string]ManifestEntry `json:"binaries"`
	// RecentReleases is the 5 newest tagged releases plus the newest of
	// each of the 5 newest minor lines (newest first), each with the commit
	// subjects between it and the previous tag. The
	// release pipeline pre-filters chore:/docs:/ci: commits — the
	// frontend renders a "general improvements" fallback when Notes is
	// empty. May be absent on older manifests.
	RecentReleases []ManifestRelease `json:"recent_releases,omitempty"`
}

// ManifestEntry describes one os/arch artifact.
type ManifestEntry struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// ManifestRelease is one entry in Manifest.RecentReleases.
type ManifestRelease struct {
	Version    string             `json:"version"`
	ReleasedAt string             `json:"released_at"`
	Notes      []ManifestNoteItem `json:"notes"`
}

// ManifestNoteItem is one commit shown in the admin upgrade panel.
type ManifestNoteItem struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
}

// UpgradeStatus is what /api/admin/upgrade/status returns. Persisted to
// <bin>.upgrade-state.json so the watchdog and the admin UI agree.
type UpgradeStatus struct {
	Phase      string    `json:"phase"` // ok | validating | rolled_back | failed | idle
	OldVersion string    `json:"old_version,omitempty"`
	NewVersion string    `json:"new_version,omitempty"`
	StartedAt  time.Time `json:"started_at,omitzero"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Error      string    `json:"error,omitempty"`
}

const (
	PhaseIdle       = "idle"
	PhaseValidating = "validating"
	PhaseOK         = "ok"
	PhaseRolledBack = "rolled_back"
	PhaseFailed     = "failed"

	backupSuffix  = ".bak"
	stagingSuffix = ".new"
	stateSuffix   = ".upgrade-state.json"
)

// DefaultManifestURL is the hard-coded location of the release manifest.
// We don't expose this in YAML — operators don't need to point this at
// anything, and a misconfigured override could silently send users to
// some attacker's binary. Release builds override this with -ldflags -X so
// a fork's binaries follow the fork's own GitHub Releases.
var DefaultManifestURL = "https://github.com/DayMug/DayMug/releases/latest/download/manifest.json"

// githubLatestDownload is the path GitHub redirects to the newest release's
// asset; githubTagDownload is the immutable per-tag form of the same asset.
const (
	githubLatestDownload = "/releases/latest/download/"
	githubTagDownload    = "/releases/download/"
)

// VersionedManifestURL turns the rolling manifest URL into the immutable
// per-tag manifest URL that the release pipeline also publishes. Used by
// `daymug upgrade --version vX.Y.Z` to pin the upgrade to a specific release
// instead of whatever the rolling URL points at right now. version is
// normalised to a leading "v" so callers can pass "1.2.3" or "v1.2.3"
// interchangeably.
func VersionedManifestURL(latestURL, version string) (string, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return "", fmt.Errorf("version is required")
	}
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	if i := strings.Index(latestURL, githubLatestDownload); i >= 0 {
		return latestURL[:i] + githubTagDownload + version + "/" + latestURL[i+len(githubLatestDownload):], nil
	}
	// Object-store layout of releases before GitHub: <base>/latest.json next
	// to <base>/<tag>/manifest.json.
	idx := strings.LastIndex(latestURL, "/")
	if idx < 0 {
		return "", fmt.Errorf("invalid manifest base URL: %s", latestURL)
	}
	return latestURL[:idx+1] + version + "/manifest.json", nil
}

// ErrUpgradeNotConfigured is returned when no Upgrader is wired up — a nil
// receiver in Check, or a handler built without one. The manifest URL itself
// is hard-coded, so production always has an Upgrader; this guards tests and
// partial wiring.
var ErrUpgradeNotConfigured = errors.New("upgrade not configured")

// DisableSelfUpgradeEnv turns every self-upgrade path off (admin check /
// apply / rollback, `daymug upgrade`). The
// container image sets it: there the binary sits in a read-only image layer
// that the next `docker pull` replaces wholesale, and no systemd/launchd
// exists for the watchdog to restart — an in-place swap could only fail or,
// worse, be silently undone by the next container recreate.
const DisableSelfUpgradeEnv = "DAYMUG_DISABLE_SELF_UPGRADE"

// ErrSelfUpgradeDisabled is what every self-upgrade entry point reports
// while DisableSelfUpgradeEnv is set. The text is shown to the admin as-is,
// so it says what to do instead.
var ErrSelfUpgradeDisabled = errors.New("self-upgrade is disabled on this install (" +
	DisableSelfUpgradeEnv + "=1); upgrade by pulling a newer image or replacing the binary")

// SelfUpgradeDisabled reports whether DisableSelfUpgradeEnv is set to a true
// value. Read on every call rather than cached so tests can use t.Setenv.
func SelfUpgradeDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(DisableSelfUpgradeEnv))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// hostPlatform is the manifest `binaries` key this binary installs from:
// "linux/amd64", "linux/arm64", "darwin/arm64", … — the same GOOS/GOARCH
// pair the release workflow names its targets by. A var so tests can
// resolve a manifest as another platform would.
var hostPlatform = runtime.GOOS + "/" + runtime.GOARCH

// ErrAlreadyUpToDate is returned by Upgrader.Check when the manifest's
// version matches the running version. The handler turns this into a
// human-readable response.
var ErrAlreadyUpToDate = errors.New("already up to date")

// ErrNoPlatformBuild reports that the manifest parsed fine and carries no
// artifact for this host's GOOS/GOARCH.
//
// This is a failure state only in the sense that there is nothing to
// install; it is not a fetch error. Callers must surface it as "this
// release has nothing for your platform" — an error banner would tell the
// admin their install is broken when the release simply never shipped a
// build for it.
var ErrNoPlatformBuild = errors.New("release has no build for this platform")

// Upgrader is the service-level entry point used by both cmd_upgrade.go
// and the admin handler. All exported methods are safe to call from a
// single goroutine; concurrent upgrades are not supported (and the admin
// endpoint serializes via a mutex).
type Upgrader struct {
	Cfg    *config.UpgradeConfig
	Client *http.Client
	// CurrentVersion is the binary's compile-time Version (passed in from
	// main, since the service package can't see it directly).
	CurrentVersion string
	// ListenAddr is the running server's listen address (e.g. ":8090" or
	// "127.0.0.1:8090"). Combined with cfg.HealthPath to build the URL the
	// post-upgrade watchdog probes.
	ListenAddr string
	// ManifestURL overrides the hard-coded DefaultManifestURL. Tests set
	// this to point at an httptest server; production leaves it empty.
	ManifestURL string
	// ConfigPath is the YAML file the running server loaded. The upgrade
	// preflight asks the staged release to validate it before the swap.
	// Empty skips the preflight (tests, config built in code).
	ConfigPath string
	// Now lets tests freeze time.
	Now func() time.Time
	// HTTPGet allows tests to stub network calls. nil falls back to Client.
	HTTPGet func(ctx context.Context, url string) (*http.Response, error)
}

// newUpgradeHTTPClient bounds a stalled server by the time to response
// headers, not the whole transfer: the binary comes from GitHub's CDN, where
// a slow but healthy link can need minutes for the full download.
func newUpgradeHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Timeout: 15 * time.Minute, Transport: transport}
}

// NewUpgrader builds an Upgrader with sensible defaults. cfg must be
// non-nil; pass &config.UpgradeConfig{} to get the validated defaults.
func NewUpgrader(cfg *config.UpgradeConfig, currentVersion, listenAddr string) *Upgrader {
	return &Upgrader{
		Cfg:            cfg,
		Client:         newUpgradeHTTPClient(),
		CurrentVersion: currentVersion,
		ListenAddr:     listenAddr,
		Now:            time.Now,
	}
}

// HealthURL builds the URL the post-upgrade watchdog polls. We always
// hit 127.0.0.1 (the watchdog runs on the same host as the service),
// using the running server's port from ListenAddr and the configured
// HealthPath. addr forms accepted: ":8090", "127.0.0.1:8090",
// "0.0.0.0:8090", "host:port".
func (u *Upgrader) HealthURL() string {
	addr := u.ListenAddr
	if addr == "" {
		addr = ":8090"
	}
	host, port := splitHostPort(addr)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	path := "/api/health"
	if u.Cfg != nil && u.Cfg.HealthPath != "" {
		path = u.Cfg.HealthPath
	}
	if port == "" {
		return "http://" + host + path
	}
	return "http://" + host + ":" + port + path
}

// manifestURL returns the override if set, otherwise the hard-coded
// DefaultManifestURL.
func (u *Upgrader) manifestURL() string {
	if u.ManifestURL != "" {
		return u.ManifestURL
	}
	return DefaultManifestURL
}

// splitHostPort tolerates the "" host (":8090") that Go's net.SplitHostPort
// rejects. Returns ("", "8090") for a leading-colon input.
func splitHostPort(addr string) (string, string) {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i], addr[i+1:]
		}
	}
	return addr, ""
}

// Check fetches the manifest and reports whether an update is available.
// Returns the parsed manifest and the matching ManifestEntry for this
// host's GOOS/GOARCH.
//
// When the manifest has no entry for this platform it returns the parsed
// manifest, a nil entry, and an error wrapping ErrNoPlatformBuild. The
// manifest is still returned so callers can tell the admin which version
// they are missing out on. Callers that treat every error as fatal will
// report a broken upgrader on a perfectly healthy Linux-only release —
// match with errors.Is first.
func (u *Upgrader) Check(ctx context.Context) (*Manifest, *ManifestEntry, error) {
	if u == nil {
		return nil, nil, ErrUpgradeNotConfigured
	}
	if SelfUpgradeDisabled() {
		return nil, nil, ErrSelfUpgradeDisabled
	}
	m, err := u.fetchManifest(ctx)
	if err != nil {
		return nil, nil, err
	}
	key := hostPlatform
	if entry, ok := m.Binaries[key]; ok {
		if entry.URL == "" || entry.SHA256 == "" {
			return m, nil, fmt.Errorf("manifest entry for %s is missing url or sha256", key)
		}
		return m, &entry, nil
	}
	return m, nil, fmt.Errorf("%w (%s)", ErrNoPlatformBuild, key)
}

// HasUpdate compares manifest.Version vs the running binary's Version,
// trimming any leading "v" so "v1.2.3" and "1.2.3" match.
func (u *Upgrader) HasUpdate(m *Manifest) bool {
	if m == nil {
		return false
	}
	return strings.TrimPrefix(m.Version, "v") != strings.TrimPrefix(u.CurrentVersion, "v")
}

func (u *Upgrader) fetchManifest(ctx context.Context) (*Manifest, error) {
	resp, err := u.httpGet(ctx, u.manifestURL())
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if m.Version == "" {
		return nil, fmt.Errorf("manifest missing version")
	}
	return &m, nil
}

func (u *Upgrader) httpGet(ctx context.Context, url string) (*http.Response, error) {
	if u.HTTPGet != nil {
		return u.HTTPGet(ctx, url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	return u.Client.Do(req)
}

// DownloadAndVerify streams entry.URL into a temp file in the same
// directory as binPath (so the eventual rename is atomic), validates its
// sha256, and returns the temp path. Caller is responsible for renaming
// or removing it.
func (u *Upgrader) DownloadAndVerify(ctx context.Context, entry *ManifestEntry, binPath string) (string, error) {
	resp, err := u.httpGet(ctx, entry.URL)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned %d", resp.StatusCode)
	}

	dir := filepath.Dir(binPath)
	tmp, err := os.CreateTemp(dir, filepath.Base(binPath)+stagingSuffix+".*")
	if err != nil {
		return "", fmt.Errorf("create staging file: %w", err)
	}
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hasher), resp.Body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("stream binary: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("chmod staging: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("close staging: %w", err)
	}
	got := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(got, entry.SHA256) {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("sha256 mismatch: got %s, want %s", got, entry.SHA256)
	}
	return tmp.Name(), nil
}

// SwapInPlace moves binPath to binPath+".bak" (overwriting any prior
// backup) then renames newPath to binPath. Both operations are within the
// same directory so the renames are atomic on POSIX filesystems. Returns
// the backup path so the caller can pass it to the watchdog.
//
// Note: on Linux you can rename a running ELF — the kernel keeps the old
// inode mapped for processes that still hold it open, while new lookups
// resolve to the freshly placed file.
func SwapInPlace(binPath, newPath string) (string, error) {
	bak := binPath + backupSuffix
	// Remove any stale backup so the rename can succeed deterministically.
	if err := os.Remove(bak); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("remove old backup: %w", err)
	}
	if err := os.Rename(binPath, bak); err != nil {
		return "", fmt.Errorf("backup current binary: %w", err)
	}
	if err := os.Rename(newPath, binPath); err != nil {
		// Best-effort rollback so we don't leave the system without a binary.
		if err2 := os.Rename(bak, binPath); err2 != nil {
			// Both failures are wrapped: this is the one path that can leave
			// the host without a usable binary, so a caller inspecting the
			// chain must be able to see the rollback failure too, not just
			// the install one.
			return "", fmt.Errorf("install new binary failed (%w) AND restore failed (%w)", err, err2)
		}
		return "", fmt.Errorf("install new binary: %w", err)
	}
	return bak, nil
}

// RestoreBackup is the inverse of SwapInPlace: copies <bin>.bak back over
// <bin>. Used by both the watchdog (auto-rollback) and the admin manual
// rollback endpoint.
func RestoreBackup(binPath string) error {
	bak := binPath + backupSuffix
	if _, err := os.Stat(bak); err != nil {
		return fmt.Errorf("no backup at %s: %w", bak, err)
	}
	// Copy (not rename) so the .bak stays around for a second rollback if
	// needed. We can't rename onto a running ELF without breaking the
	// running process — but that's fine because the caller is about to
	// systemctl restart anyway.
	in, err := os.Open(bak)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(binPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// SelfHealUpgradeState rewrites a stuck `validating` state to `ok`
// when the running binary already matches the target version. The
// watchdog can be killed (cgroup teardown, OOM, network blip) before
// it gets to write the success marker; without this the admin UI
// would show "Validating new binary…" forever even though the new
// binary booted fine.
//
// Called once at server startup. Only acts when:
//   - state file exists and is parseable
//   - phase == validating
//   - state.NewVersion matches currentVersion (modulo leading "v")
//
// Any disk error is non-fatal — startup must not fail because of a
// stale state file.
func SelfHealUpgradeState(binPath, currentVersion string) {
	s, err := ReadStatus(binPath)
	if err != nil {
		return
	}
	if s.Phase != PhaseValidating {
		return
	}
	if strings.TrimPrefix(s.NewVersion, "v") != strings.TrimPrefix(currentVersion, "v") {
		return
	}
	_ = WriteStatus(binPath, UpgradeStatus{
		Phase:      PhaseOK,
		OldVersion: s.OldVersion,
		NewVersion: s.NewVersion,
		StartedAt:  s.StartedAt,
		FinishedAt: time.Now().UTC(),
	})
}

// WriteStatus persists s to <binPath><stateSuffix>. Used by the apply
// handler (write "validating") and the watchdog (write "ok" or
// "rolled_back").
func WriteStatus(binPath string, s UpgradeStatus) error {
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(StatePath(binPath), body, 0o644)
}

// ReadStatus loads the persisted state. Returns PhaseIdle if the file
// doesn't exist; that's the default for a never-upgraded install.
func ReadStatus(binPath string) (UpgradeStatus, error) {
	body, err := os.ReadFile(StatePath(binPath))
	if errors.Is(err, os.ErrNotExist) {
		return UpgradeStatus{Phase: PhaseIdle}, nil
	}
	if err != nil {
		return UpgradeStatus{}, err
	}
	var s UpgradeStatus
	if err := json.Unmarshal(body, &s); err != nil {
		return UpgradeStatus{}, err
	}
	if s.Phase == "" {
		s.Phase = PhaseIdle
	}
	return s, nil
}

// StatePath is exported so tests and the CLI watchdog can compute the
// same path without duplicating the suffix.
func StatePath(binPath string) string {
	return binPath + stateSuffix
}

// BackupPath returns the conventional <bin>.bak path.
func BackupPath(binPath string) string {
	return binPath + backupSuffix
}

// SpawnWatchdog launches `<bakPath> upgrade-watchdog ...` as a detached
// process so it survives the parent's death (which happens almost
// immediately after the parent restarts the service). Returns once the
// child has been started; the child does its work in the background.
//
// We invoke from <bin>.bak (the OLD binary) rather than <bin> (the NEW
// binary) because the OLD binary is known-good. If the NEW binary panics
// on startup, the OLD binary's watchdog can still drive the rollback.
//
// Cgroup escape (Linux only): when the parent runs under systemd, a
// plain fork+setsid is not enough — `systemctl restart daymug` SIGKILLs
// every process in the service's cgroup, watchdog included. We shell
// out to `systemd-run --user|--system` to launch the watchdog inside
// its own transient unit so it survives the restart.
//
// On macOS no equivalent escape is needed: `launchctl kickstart -k`
// only signals the job's leader PID, and a plain `Setsid` puts the
// watchdog into its own session/process group so it gets orphaned to
// PID 1 instead of being killed alongside the leader.
func SpawnWatchdog(bakPath, binPath, healthURL, oldVersion, newVersion, service, mode string, healthTimeout time.Duration) error {
	watchdogArgs := []string{
		"upgrade-watchdog",
		"--bin", binPath,
		"--health-url", healthURL,
		"--old-version", oldVersion,
		"--new-version", newVersion,
		"--service", service,
		"--service-mode", mode,
		"--timeout", healthTimeout.String(),
		// The watchdog snapshots the database between stopping the old
		// server and starting the new one. Both binaries share a directory,
		// so this resolves to the file the running server has open.
		"--db", config.DefaultDBPath(),
	}

	name, args, useSystemdRun := watchdogCommand(bakPath, mode, watchdogArgs, time.Now().UnixNano())
	cmd := exec.Command(name, args...) // #nosec G204 -- bakPath/mode are server-controlled.
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if !useSystemdRun {
		// Direct exec: detach into a new session so the parent's TTY can't
		// signal us. Under systemd-run this is unnecessary because the
		// transient unit owns the new process.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn watchdog: %w", err)
	}
	// Detach so we don't accumulate a zombie if the parent doesn't get
	// killed by systemd in time to wait().
	go func() { _ = cmd.Process.Release() }()
	return nil
}

// watchdogCommand builds the argv used to launch the watchdog. When the
// parent runs under systemd we wrap the call in `systemd-run` so the
// watchdog's own cgroup is decoupled from the service's — otherwise the
// upcoming `systemctl restart` would SIGKILL the watchdog along with the
// old server. Falls back to a plain exec when systemd-run is unavailable
// (non-Linux hosts, container images stripped of systemd) so unit tests
// and trimmed deployments still work.
//
// Exported as a free function (rather than inlined into SpawnWatchdog)
// purely so tests can assert the argv shape without spawning a process.
func watchdogCommand(bakPath, mode string, watchdogArgs []string, nonce int64) (name string, args []string, useSystemdRun bool) {
	if (mode == "user" || mode == "system") && runtime.GOOS == "linux" {
		if path, err := exec.LookPath("systemd-run"); err == nil {
			run := []string{}
			if mode == "user" {
				run = append(run, "--user")
			}
			run = append(run,
				"--quiet",
				"--collect",
				"--unit", fmt.Sprintf("daymug-upgrade-watchdog-%d", nonce),
				"--description", "daymug upgrade watchdog",
				"--",
				bakPath,
			)
			run = append(run, watchdogArgs...)
			return path, run, true
		}
	}
	return bakPath, watchdogArgs, false
}

// DarwinLaunchdLabel is the launchd Label used by the LaunchAgent /
// LaunchDaemon plist that `daymug bootstrap` writes on macOS.
// Hard-coded because we always ship a single plist; the systemd-style
// `upgrade.service` YAML key is ignored on darwin and the matching
// `launchctl ... gui/<uid>/<label>` (or `system/<label>`) target is
// derived from this constant plus the detected ServiceMode.
const DarwinLaunchdLabel = "com.daymug.daymug"

// RestartService restarts the running daymug service so launchd /
// systemd respawns it from the freshly swapped binary. Returns the
// combined diagnostic output (mostly for systemctl); on macOS we kill
// the pid directly so the slice is usually nil.
//
// Linux: `systemctl [--user] restart <service>`.
//
// macOS: looks up the service's leader PID via `launchctl print` and
// SIGKILLs it; the plist's KeepAlive=true tells launchd to respawn from
// <bin> on the next boot cycle. We do NOT shell out to
// `launchctl kickstart -k`: when called from a setsid-detached watchdog
// process, launchctl rejects GUI-domain commands with "Operation not
// permitted" because the watchdog is no longer in the user's Aqua audit
// session. SIGKILL is a UID check, not a session check, so it works
// from the watchdog the same as it does from the admin's terminal.
func RestartService(service, mode string) ([]byte, error) {
	if runtime.GOOS == "darwin" {
		return darwinRestart(DarwinLaunchdLabel, mode, os.Getuid())
	}
	name, args := restartCommand(runtime.GOOS, service, mode, os.Getuid())
	return exec.Command(name, args...).CombinedOutput() // #nosec G204 -- mode/service from validated config.
}

// darwinRestart implements the macOS branch of RestartService. Split
// out so the surrounding logic stays readable on Linux too.
func darwinRestart(label, mode string, uid int) ([]byte, error) {
	target := launchdTarget(mode, label, uid)
	out, err := exec.Command("launchctl", "print", target).CombinedOutput() // #nosec G204 -- target derived from validated config.
	if err != nil {
		return out, fmt.Errorf("launchctl print %s: %w", target, err)
	}
	pid, err := parseLaunchctlPID(string(out))
	if err != nil {
		// Service is loaded but currently has no leader PID (e.g. it
		// already exited and launchd is mid-respawn). Nothing to kill;
		// KeepAlive will boot the new binary on its own.
		return out, nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return out, fmt.Errorf("find pid %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		return out, fmt.Errorf("kill pid %d: %w", pid, err)
	}
	return out, nil
}

// parseLaunchctlPID extracts the leader PID from `launchctl print
// <target>` output. The relevant line looks like `\tpid = 12345` (tab
// indent + lowercase key). Returns an error if no such line is present
// — the caller treats that as "service has no current PID, nothing to
// kill".
func parseLaunchctlPID(out string) (int, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "pid = ")
		if !ok {
			continue
		}
		if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
			rest = rest[:idx]
		}
		n, err := strconv.Atoi(rest)
		if err != nil {
			return 0, fmt.Errorf("parse pid %q: %w", rest, err)
		}
		if n <= 0 {
			return 0, fmt.Errorf("invalid pid %d", n)
		}
		return n, nil
	}
	return 0, fmt.Errorf("no pid line in launchctl print output")
}

// restartCommand builds the argv used by the Linux branch of
// RestartService. Exported as a free function (rather than inlined) so
// tests can assert argv shape without spawning a process. The macOS
// branch lives in darwinRestart instead — kickstart is unreliable when
// called from a session-detached watchdog (see RestartService doc).
func restartCommand(goos, service, mode string, uid int) (name string, args []string) {
	_ = goos
	_ = uid
	sysArgs := []string{}
	if mode == "user" {
		sysArgs = append(sysArgs, "--user")
	}
	sysArgs = append(sysArgs, "restart", service)
	return "systemctl", sysArgs
}

// launchdTarget builds the launchctl domain-target string. mode "system"
// targets the system-wide LaunchDaemon (requires root); anything else
// targets the per-user LaunchAgent in the GUI session for uid. The two
// domains are distinct: a LaunchAgent loaded into gui/<uid> is invisible
// to `launchctl ... system/<label>` and vice versa, so the mode must
// match the actual install variant.
func launchdTarget(mode, label string, uid int) string {
	if mode == "system" {
		return "system/" + label
	}
	return fmt.Sprintf("gui/%d/%s", uid, label)
}

// DetectLaunchdMode probes for the conventional launchd plist on macOS
// and returns "system" or "user" depending on which install variant is
// present. Returns "" on non-darwin hosts and when neither plist is
// installed; the caller falls back to the YAML-configured ServiceMode.
//
// System install wins over user install when both happen to exist (the
// system daemon is the more privileged, longer-running one). Called
// once at server startup so the upgrade flow can pick the right
// `launchctl` domain target without operators having to keep
// `upgrade.service_mode` in YAML in sync with how they actually ran
// `daymug bootstrap`.
func DetectLaunchdMode() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	agentDir := ""
	if home != "" {
		agentDir = filepath.Join(home, "Library", "LaunchAgents")
	}
	return detectLaunchdMode("/Library/LaunchDaemons", agentDir)
}

// detectLaunchdMode is the testable core of DetectLaunchdMode. Either
// directory may be empty (skipped); both empty returns "".
func detectLaunchdMode(daemonDir, agentDir string) string {
	if daemonDir != "" {
		if _, err := os.Stat(filepath.Join(daemonDir, DarwinLaunchdLabel+".plist")); err == nil {
			return "system"
		}
	}
	if agentDir != "" {
		if _, err := os.Stat(filepath.Join(agentDir, DarwinLaunchdLabel+".plist")); err == nil {
			return "user"
		}
	}
	return ""
}

// ProbeHealth polls healthURL with the given client until it returns 200
// or the context fires. Returns nil on the first 200 response.
func ProbeHealth(ctx context.Context, client *http.Client, healthURL string) error {
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	deadline := time.Now().Add(60 * time.Second)
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, http.NoBody)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("health probe failed: %w", err)
			}
			return fmt.Errorf("health probe returned non-200")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}
