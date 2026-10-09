package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/config"
)

func TestUpgraderCheckNilReceiver(t *testing.T) {
	var u *Upgrader
	if _, _, err := u.Check(context.Background()); !errors.Is(err, ErrUpgradeNotConfigured) {
		t.Fatalf("nil receiver: got %v, want %v", err, ErrUpgradeNotConfigured)
	}
}

// newTestUpgrader builds an Upgrader pointed at a manifest-serving
// httptest server. Tests override the hard-coded DefaultManifestURL by
// setting Upgrader.ManifestURL.
func newTestUpgrader(cfgManifestURL string) *Upgrader {
	u := NewUpgrader(&config.UpgradeConfig{}, "v1.0.0", ":8080")
	u.ManifestURL = cfgManifestURL
	return u
}

func TestUpgraderCheckParsesManifest(t *testing.T) {
	manifest := fmt.Sprintf(`{
  "version": "v2.0.0",
  "released_at": "2026-04-30T00:00:00Z",
  "binaries": {
    "%s/%s": {"url": "https://example.com/bin", "sha256": "abc"}
  }
}`, runtime.GOOS, runtime.GOARCH)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, manifest)
	}))
	defer srv.Close()

	u := newTestUpgrader(srv.URL)
	m, entry, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if m.Version != "v2.0.0" {
		t.Errorf("manifest.Version: got %q want v2.0.0", m.Version)
	}
	if entry == nil || entry.URL != "https://example.com/bin" {
		t.Errorf("entry: %+v", entry)
	}
	if !u.HasUpdate(m) {
		t.Errorf("HasUpdate should be true (v1 -> v2)")
	}
}

func TestUpgraderCheckParsesRecentReleases(t *testing.T) {
	manifest := fmt.Sprintf(`{
  "version": "v2.0.0",
  "released_at": "2026-05-22T00:00:00Z",
  "binaries": {
    "%s/%s": {"url": "https://example.com/bin", "sha256": "abc"}
  },
  "recent_releases": [
    {
      "version": "v2.0.0",
      "released_at": "2026-05-22T00:00:00Z",
      "notes": [
        {"hash": "abc1234", "subject": "feat: new admin panel"},
        {"hash": "def5678", "subject": "fix: race in upgrader"}
      ]
    },
    {
      "version": "v1.9.0",
      "released_at": "2026-04-30T00:00:00Z",
      "notes": []
    }
  ]
}`, runtime.GOOS, runtime.GOARCH)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, manifest)
	}))
	defer srv.Close()

	u := newTestUpgrader(srv.URL)
	m, _, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got := len(m.RecentReleases); got != 2 {
		t.Fatalf("RecentReleases length: got %d want 2", got)
	}
	first := m.RecentReleases[0]
	if first.Version != "v2.0.0" {
		t.Errorf("first release version: got %q want v2.0.0", first.Version)
	}
	if len(first.Notes) != 2 || first.Notes[0].Hash != "abc1234" ||
		first.Notes[0].Subject != "feat: new admin panel" {
		t.Errorf("first release notes: %+v", first.Notes)
	}
	if len(m.RecentReleases[1].Notes) != 0 {
		t.Errorf("v1.9.0 notes should be empty, got %+v", m.RecentReleases[1].Notes)
	}
}

// Older manifests don't carry recent_releases. Make sure the upgrader
// still parses cleanly and exposes a nil slice rather than blowing up.
func TestUpgraderCheckAcceptsManifestWithoutRecentReleases(t *testing.T) {
	manifest := fmt.Sprintf(`{
  "version": "v2.0.0",
  "released_at": "2026-04-30T00:00:00Z",
  "binaries": {
    "%s/%s": {"url": "https://example.com/bin", "sha256": "abc"}
  }
}`, runtime.GOOS, runtime.GOARCH)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, manifest)
	}))
	defer srv.Close()

	u := newTestUpgrader(srv.URL)
	m, _, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if m.RecentReleases != nil {
		t.Errorf("RecentReleases should be nil on legacy manifest, got %+v", m.RecentReleases)
	}
}

func TestVersionedManifestURL(t *testing.T) {
	tests := []struct {
		name      string
		latestURL string
		version   string
		want      string
		wantErr   bool
	}{
		{
			name:      "normalises bare version",
			latestURL: "https://mirror.example.com/daymug/latest.json",
			version:   "1.2.3",
			want:      "https://mirror.example.com/daymug/v1.2.3/manifest.json",
		},
		{
			name:      "passes through v-prefixed version",
			latestURL: "https://mirror.example.com/daymug/latest.json",
			version:   "v1.2.3",
			want:      "https://mirror.example.com/daymug/v1.2.3/manifest.json",
		},
		{
			name:      "trims surrounding whitespace",
			latestURL: "https://mirror.example.com/daymug/latest.json",
			version:   "  v1.2.3  ",
			want:      "https://mirror.example.com/daymug/v1.2.3/manifest.json",
		},
		{
			name:      "github latest download becomes the tag download",
			latestURL: "https://github.com/DayMug/DayMug/releases/latest/download/manifest.json",
			version:   "1.2.3",
			want:      "https://github.com/DayMug/DayMug/releases/download/v1.2.3/manifest.json",
		},
		{
			name:      "works with custom base path",
			latestURL: "https://example.com/foo/bar/latest.json",
			version:   "v0.0.1",
			want:      "https://example.com/foo/bar/v0.0.1/manifest.json",
		},
		{
			name:      "rejects empty version",
			latestURL: "https://mirror.example.com/daymug/latest.json",
			version:   "",
			wantErr:   true,
		},
		{
			name:      "rejects URL without slash",
			latestURL: "latest.json",
			version:   "v1",
			wantErr:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := VersionedManifestURL(tc.latestURL, tc.version)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (got %q)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUpgraderHasUpdateTrimsLeadingV(t *testing.T) {
	u := newTestUpgrader("x")
	if u.HasUpdate(&Manifest{Version: "1.0.0"}) {
		t.Errorf("v1.0.0 vs 1.0.0 should be equal")
	}
	if !u.HasUpdate(&Manifest{Version: "1.0.1"}) {
		t.Errorf("v1.0.0 vs 1.0.1 should differ")
	}
}

// A manifest that omits this host's platform is the normal shape of a
// Linux-only release seen from a Mac. Check must report it through the
// ErrNoPlatformBuild sentinel — and still hand back the manifest, so the
// caller can name the version the platform is missing out on.
func TestUpgraderCheckMissingArchEntry(t *testing.T) {
	manifest := `{"version":"v2.0.0","binaries":{"plan9/sparc":{"url":"u","sha256":"s"}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, manifest)
	}))
	defer srv.Close()

	u := newTestUpgrader(srv.URL)
	m, entry, err := u.Check(context.Background())
	if !errors.Is(err, ErrNoPlatformBuild) {
		t.Fatalf("expected ErrNoPlatformBuild, got %v", err)
	}
	if entry != nil {
		t.Fatalf("expected nil entry, got %+v", entry)
	}
	if m == nil || m.Version != "v2.0.0" {
		t.Fatalf("expected the manifest to survive the miss, got %+v", m)
	}
	if want := runtime.GOOS + "/" + runtime.GOARCH; !strings.Contains(err.Error(), want) {
		t.Fatalf("expected %q in error %q", want, err.Error())
	}
}

// The release workflow publishes linux/amd64, linux/arm64 and (sometimes)
// darwin/arm64 side by side under GOOS/GOARCH keys; each host must pick its
// own entry and never a sibling arch's.
func TestUpgraderCheckPicksHostPlatformEntry(t *testing.T) {
	manifest := `{"version":"v2.0.0","binaries":{
  "linux/amd64":  {"url":"https://x/daymug-linux-amd64",  "sha256":"a"},
  "linux/arm64":  {"url":"https://x/daymug-linux-arm64",  "sha256":"b"},
  "darwin/arm64": {"url":"https://x/daymug-darwin-arm64", "sha256":"c"}
}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, manifest)
	}))
	defer srv.Close()

	orig := hostPlatform
	t.Cleanup(func() { hostPlatform = orig })
	for _, platform := range []string{"linux/amd64", "linux/arm64", "darwin/arm64"} {
		hostPlatform = platform
		_, entry, err := newTestUpgrader(srv.URL).Check(context.Background())
		if err != nil {
			t.Fatalf("%s: Check: %v", platform, err)
		}
		want := "https://x/daymug-" + strings.ReplaceAll(platform, "/", "-")
		if entry == nil || entry.URL != want {
			t.Fatalf("%s: entry = %+v, want url %s", platform, entry, want)
		}
	}
}

func TestUpgraderCheckRefusesWhenSelfUpgradeDisabled(t *testing.T) {
	t.Setenv(DisableSelfUpgradeEnv, "1")
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("manifest fetched while self-upgrade is disabled")
	}))
	defer srv.Close()

	if _, _, err := newTestUpgrader(srv.URL).Check(context.Background()); !errors.Is(err, ErrSelfUpgradeDisabled) {
		t.Fatalf("Check: got %v, want %v", err, ErrSelfUpgradeDisabled)
	}
}

func TestSelfUpgradeDisabledValues(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false}, {"0", false}, {"false", false}, {"no", false},
		{"1", true}, {"true", true}, {"TRUE", true}, {"yes", true}, {" 1 ", true},
	} {
		t.Setenv(DisableSelfUpgradeEnv, tc.value)
		if got := SelfUpgradeDisabled(); got != tc.want {
			t.Errorf("%s=%q: got %v want %v", DisableSelfUpgradeEnv, tc.value, got, tc.want)
		}
	}
}

func TestDownloadAndVerifyHappy(t *testing.T) {
	payload := []byte("fake binary contents")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	bin := filepath.Join(dir, "daymug")
	u := newTestUpgrader("x")
	tmp, err := u.DownloadAndVerify(context.Background(), &ManifestEntry{
		URL:    srv.URL,
		SHA256: hex.EncodeToString(sum[:]),
	}, bin)
	if err != nil {
		t.Fatalf("DownloadAndVerify: %v", err)
	}
	got, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("read tmp: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload roundtrip mismatch")
	}
	// Staging file must land in the same directory as bin so the eventual
	// rename is atomic.
	if filepath.Dir(tmp) != dir {
		t.Errorf("tmp dir: got %q want %q", filepath.Dir(tmp), dir)
	}
	info, err := os.Stat(tmp)
	if err != nil {
		t.Fatalf("stat tmp: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("staging file should be executable, got mode %v", info.Mode())
	}
}

func TestDownloadAndVerifySHAMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("contents"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	bin := filepath.Join(dir, "daymug")
	u := newTestUpgrader("x")
	_, err := u.DownloadAndVerify(context.Background(), &ManifestEntry{
		URL:    srv.URL,
		SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
	}, bin)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("expected sha mismatch, got %v", err)
	}
	// Failed download must not leave a staging file lying around.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), stagingSuffix) {
			t.Errorf("staging file leaked: %s", e.Name())
		}
	}
}

func TestSwapInPlaceRoundtrip(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "daymug")
	if err := os.WriteFile(bin, []byte("OLD"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	newPath := filepath.Join(dir, "daymug.new.123")
	if err := os.WriteFile(newPath, []byte("NEW"), 0o755); err != nil {
		t.Fatalf("seed new: %v", err)
	}

	bak, err := SwapInPlace(bin, newPath)
	if err != nil {
		t.Fatalf("SwapInPlace: %v", err)
	}
	if bak != bin+".bak" {
		t.Errorf("backup path: got %q", bak)
	}
	got, _ := os.ReadFile(bin)
	if string(got) != "NEW" {
		t.Errorf("active binary: got %q want NEW", got)
	}
	gotBak, _ := os.ReadFile(bak)
	if string(gotBak) != "OLD" {
		t.Errorf("backup binary: got %q want OLD", gotBak)
	}

	// Restoring should put OLD back in place.
	if err := RestoreBackup(bin); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	got, _ = os.ReadFile(bin)
	if string(got) != "OLD" {
		t.Errorf("restored binary: got %q want OLD", got)
	}
}

func TestSwapInPlaceOverwritesStaleBackup(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "daymug")
	stale := bin + ".bak"
	if err := os.WriteFile(bin, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("STALE"), 0o755); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(dir, "daymug.new")
	if err := os.WriteFile(newPath, []byte("NEW"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := SwapInPlace(bin, newPath); err != nil {
		t.Fatalf("SwapInPlace: %v", err)
	}
	// .bak should now hold OLD, not STALE.
	got, _ := os.ReadFile(stale)
	if string(got) != "OLD" {
		t.Errorf("stale backup not overwritten: got %q", got)
	}
}

func TestStatusRoundtrip(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "daymug")
	// Reading before any write returns idle, no error.
	s, err := ReadStatus(bin)
	if err != nil {
		t.Fatalf("ReadStatus on missing: %v", err)
	}
	if s.Phase != PhaseIdle {
		t.Errorf("missing state should be idle, got %q", s.Phase)
	}
	want := UpgradeStatus{
		Phase:      PhaseValidating,
		OldVersion: "v1.0.0",
		NewVersion: "v1.0.1",
	}
	if err := WriteStatus(bin, want); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	got, err := ReadStatus(bin)
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if got.Phase != want.Phase || got.OldVersion != want.OldVersion || got.NewVersion != want.NewVersion {
		t.Errorf("status mismatch: %+v vs %+v", got, want)
	}
}

func TestWatchdogCommandUsesSystemdRunOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd-run wrapping is Linux-only")
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip("systemd-run not available on this host")
	}

	watchdogArgs := []string{"upgrade-watchdog", "--bin", "/path/to/daymug"}
	name, args, used := watchdogCommand("/path/to/daymug.bak", "user", watchdogArgs, 42)
	if !used {
		t.Fatalf("expected systemd-run wrapping when systemd-run is on PATH")
	}
	if !strings.HasSuffix(name, "systemd-run") {
		t.Errorf("name: got %q, want suffix systemd-run", name)
	}
	// Required flags so the unit cleans up and runs in the user manager.
	wantFlags := []string{"--user", "--quiet", "--collect", "--unit", "daymug-upgrade-watchdog-42"}
	for _, want := range wantFlags {
		if !slices.Contains(args, want) {
			t.Errorf("argv missing %q: %v", want, args)
		}
	}
	// The actual watchdog invocation must come after a `--` separator so
	// systemd-run doesn't try to interpret watchdog flags.
	sepIdx := -1
	for i, a := range args {
		if a == "--" {
			sepIdx = i
			break
		}
	}
	if sepIdx < 0 {
		t.Fatalf("argv missing -- separator: %v", args)
	}
	if sepIdx+1 >= len(args) || args[sepIdx+1] != "/path/to/daymug.bak" {
		t.Errorf("expected bakPath right after --, got %v", args[sepIdx:])
	}
}

func TestWatchdogCommandFallsBackToDirectExec(t *testing.T) {
	// "" mode signals "no systemd integration" — we should run the bak
	// binary directly without any wrapper.
	watchdogArgs := []string{"upgrade-watchdog", "--bin", "/path/to/daymug"}
	name, args, used := watchdogCommand("/path/to/daymug.bak", "", watchdogArgs, 0)
	if used {
		t.Fatalf("expected direct exec when mode is empty")
	}
	if name != "/path/to/daymug.bak" {
		t.Errorf("name: got %q, want bakPath", name)
	}
	if len(args) != len(watchdogArgs) || args[0] != "upgrade-watchdog" {
		t.Errorf("args: got %v, want %v", args, watchdogArgs)
	}
}

func TestSelfHealUpgradeStateRewritesStuckValidating(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "daymug")
	if err := WriteStatus(bin, UpgradeStatus{
		Phase:      PhaseValidating,
		OldVersion: "v0.5.3",
		NewVersion: "v0.5.5",
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	SelfHealUpgradeState(bin, "v0.5.5")

	got, err := ReadStatus(bin)
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if got.Phase != PhaseOK {
		t.Errorf("phase: got %q, want %q", got.Phase, PhaseOK)
	}
	if got.NewVersion != "v0.5.5" || got.OldVersion != "v0.5.3" {
		t.Errorf("versions not preserved: %+v", got)
	}
	if got.FinishedAt.IsZero() {
		t.Errorf("finished_at should be stamped")
	}
}

func TestSelfHealUpgradeStateLeavesMismatchedVersion(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "daymug")
	if err := WriteStatus(bin, UpgradeStatus{
		Phase:      PhaseValidating,
		NewVersion: "v0.5.5",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Running binary is v0.5.4 — DOES NOT match the target. Don't heal;
	// the rollback path is responsible for this case.
	SelfHealUpgradeState(bin, "v0.5.4")
	got, _ := ReadStatus(bin)
	if got.Phase != PhaseValidating {
		t.Errorf("phase changed unexpectedly: %q", got.Phase)
	}
}

func TestSelfHealUpgradeStateNoFile(t *testing.T) {
	dir := t.TempDir()
	// No state file present — must not panic and must not create a file.
	SelfHealUpgradeState(filepath.Join(dir, "daymug"), "v0.5.5")
	if _, err := os.Stat(filepath.Join(dir, "daymug"+stateSuffix)); !os.IsNotExist(err) {
		t.Errorf("state file should not be created when none existed")
	}
}

func TestProbeHealthSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ProbeHealth(ctx, srv.Client(), srv.URL); err != nil {
		t.Fatalf("ProbeHealth: %v", err)
	}
}

func TestRestartCommand(t *testing.T) {
	tests := []struct {
		name     string
		service  string
		mode     string
		wantArgs []string
	}{
		{
			name:     "user mode adds --user",
			service:  "daymug",
			mode:     "user",
			wantArgs: []string{"--user", "restart", "daymug"},
		},
		{
			name:     "system mode omits --user",
			service:  "daymug",
			mode:     "system",
			wantArgs: []string{"restart", "daymug"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, args := restartCommand("linux", tc.service, tc.mode, 0)
			if name != "systemctl" {
				t.Errorf("name: got %q want systemctl", name)
			}
			if !slices.Equal(args, tc.wantArgs) {
				t.Errorf("args: got %v want %v", args, tc.wantArgs)
			}
		})
	}
}

func TestParseLaunchctlPID(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    int
		wantErr bool
	}{
		{
			name: "typical launchctl print snippet",
			out: `com.daymug.daymug = {
	type = Aqua
	domain = gui/501/com.daymug.daymug
	asid = 100009
	pid = 12345
	state = running
}`,
			want: 12345,
		},
		{
			name: "no leading whitespace",
			out:  "pid = 7",
			want: 7,
		},
		{
			name: "trailing comment after pid",
			out:  "pid = 99 # something",
			want: 99,
		},
		{
			name:    "no pid line",
			out:     "type = Aqua\nstate = exited",
			wantErr: true,
		},
		{
			name:    "non-numeric pid",
			out:     "pid = NaN",
			wantErr: true,
		},
		{
			name:    "non-positive pid is rejected",
			out:     "pid = 0",
			wantErr: true,
		},
		{
			name: "ignores ParentPID-like keys that contain pid as substring",
			out: `domain = gui/501/com.daymug.daymug
ParentPID = 1
pid = 4242`,
			want: 4242,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLaunchctlPID(tc.out)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got pid=%d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestDetectLaunchdMode(t *testing.T) {
	plistName := DarwinLaunchdLabel + ".plist"

	t.Run("system plist wins over user plist", func(t *testing.T) {
		daemonDir := t.TempDir()
		agentDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(daemonDir, plistName), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(agentDir, plistName), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := detectLaunchdMode(daemonDir, agentDir); got != "system" {
			t.Errorf("got %q want %q", got, "system")
		}
	})

	t.Run("user plist only", func(t *testing.T) {
		daemonDir := t.TempDir()
		agentDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(agentDir, plistName), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := detectLaunchdMode(daemonDir, agentDir); got != "user" {
			t.Errorf("got %q want %q", got, "user")
		}
	})

	t.Run("system plist only", func(t *testing.T) {
		daemonDir := t.TempDir()
		agentDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(daemonDir, plistName), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := detectLaunchdMode(daemonDir, agentDir); got != "system" {
			t.Errorf("got %q want %q", got, "system")
		}
	})

	t.Run("neither installed returns empty", func(t *testing.T) {
		daemonDir := t.TempDir()
		agentDir := t.TempDir()
		if got := detectLaunchdMode(daemonDir, agentDir); got != "" {
			t.Errorf("got %q want empty", got)
		}
	})

	t.Run("empty agent dir is skipped (no os.UserHomeDir)", func(t *testing.T) {
		daemonDir := t.TempDir()
		// agentDir = "" mirrors the real-world case where os.UserHomeDir
		// fails (root daemons running with HOME unset). Detection should
		// still surface a system install if one is present.
		if err := os.WriteFile(filepath.Join(daemonDir, plistName), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := detectLaunchdMode(daemonDir, ""); got != "system" {
			t.Errorf("got %q want %q", got, "system")
		}
	})
}

func TestDetectLaunchdModePublicReturnsEmptyOnNonDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("public DetectLaunchdMode only short-circuits on non-darwin")
	}
	if got := DetectLaunchdMode(); got != "" {
		t.Errorf("DetectLaunchdMode on %s returned %q, want empty", runtime.GOOS, got)
	}
}
