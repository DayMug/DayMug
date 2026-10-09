package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
)

// upgradeRouter mounts only the /api/admin/upgrade/* routes against a
// hand-rolled AdminUpgradeHandler so we can swap in a tempdir bin path
// without touching os.Executable() (which would point at the test binary).
func upgradeRouter(t *testing.T, h *AdminUpgradeHandler) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/admin")
	g.Use(adminAuthCtx("admin-1", true))
	g.Use(middleware.RequireAdmin())
	g.GET("/upgrade/check", h.Check)
	g.POST("/upgrade/apply", h.Apply)
	g.POST("/upgrade/restart", h.Restart)
	g.POST("/upgrade/rollback", h.Rollback)
	g.GET("/upgrade/status", h.Status)
	return r
}

func TestUpgradeCheckNotConfigured(t *testing.T) {
	h := &AdminUpgradeHandler{}
	r := upgradeRouter(t, h)
	w := doJSON(r, "GET", "/api/admin/upgrade/check", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", w.Code)
	}
}

func TestNewAdminUpgradeHandlerUsesThreeSecondQuietWindow(t *testing.T) {
	h := NewAdminUpgradeHandler(nil, service.NewDrainer())
	if h.gracefulDelay != 3*time.Second {
		t.Fatalf("graceful delay = %v, want 3s", h.gracefulDelay)
	}
}

// stubbedHandler builds an AdminUpgradeHandler whose upgrader points at
// an in-test httptest server serving the given manifest, and whose
// BinPath lives in a tempdir (with a fake "OLD" binary already in place).
func stubbedHandler(t *testing.T, manifest string) (*AdminUpgradeHandler, string, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "daymug")
	if err := os.WriteFile(bin, []byte("OLD"), 0o755); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, manifest)
	}))
	cfg := &config.UpgradeConfig{
		Service:       "daymug",
		ServiceMode:   "user",
		HealthPath:    "/api/health",
		HealthTimeout: config.Duration{},
	}
	upgrader := service.NewUpgrader(cfg, "v1.0.0", "127.0.0.1:1")
	upgrader.ManifestURL = srv.URL
	return &AdminUpgradeHandler{
		Upgrader: upgrader,
		BinPath:  bin,
	}, bin, srv
}

func waitUpgradePhase(t *testing.T, h *AdminUpgradeHandler, phase string) service.UpgradeStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last service.UpgradeStatus
	var lastErr error
	for time.Now().Before(deadline) {
		last, lastErr = service.ReadStatus(h.BinPath)
		if lastErr == nil && last.Phase == phase {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("phase = %q err=%v, want %q", last.Phase, lastErr, phase)
	return last
}

func TestUpgradeCheckReportsLatest(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v2.0.0","released_at":"2026-04-30T00:00:00Z","binaries":{"%s/%s":{"url":"https://x/bin","sha256":"deadbeef"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	r := upgradeRouter(t, h)
	w := doJSON(r, "GET", "/api/admin/upgrade/check", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["latest_version"] != "v2.0.0" {
		t.Errorf("latest_version: %v", out["latest_version"])
	}
	if out["has_update"] != true {
		t.Errorf("has_update: %v", out["has_update"])
	}
	if out["current_version"] != "v1.0.0" {
		t.Errorf("current_version: %v", out["current_version"])
	}
}

// A Linux-only release seen from a Mac: the manifest advertises a newer
// version but carries nothing this host can install. That must read as a
// 200 the panel can explain, not a 502 that looks like a broken upgrader.
func TestUpgradeCheckReportsUnsupportedPlatform(t *testing.T) {
	manifest := `{"version":"v2.0.0","released_at":"2026-04-30T00:00:00Z","binaries":{"plan9/sparc":{"url":"https://x/bin","sha256":"deadbeef"}}}`
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	r := upgradeRouter(t, h)
	w := doJSON(r, "GET", "/api/admin/upgrade/check", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200 body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["platform_supported"] != false {
		t.Errorf("platform_supported: %v", out["platform_supported"])
	}
	// Nothing to download, so Update must stay disabled even though the
	// manifest is ahead of the running binary.
	if out["has_update"] != false {
		t.Errorf("has_update: %v", out["has_update"])
	}
	if out["latest_version"] != "v2.0.0" {
		t.Errorf("latest_version: %v", out["latest_version"])
	}
	if out["download_url"] != "" {
		t.Errorf("download_url: %v", out["download_url"])
	}
}

// Apply must refuse before draining: draining stops accepting prompts, and
// an upgrade with no artifact will never come back to release the gate.
func TestUpgradeApplyRefusesUnsupportedPlatform(t *testing.T) {
	manifest := `{"version":"v2.0.0","binaries":{"plan9/sparc":{"url":"https://x/bin","sha256":"deadbeef"}}}`
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	h.Drainer = service.NewDrainer()

	r := upgradeRouter(t, h)
	w := doJSON(r, "POST", "/api/admin/upgrade/apply", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200 body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["status"] != "no_platform_build" {
		t.Fatalf("status: %v body=%s", out["status"], w.Body.String())
	}
	if h.Drainer.IsDraining() {
		t.Error("apply must not start draining when there is nothing to install")
	}
	if _, _, ok := h.Drainer.TryJobStartWithInfo(service.DrainerJob{Username: "alice"}); !ok {
		t.Error("prompts must still be accepted after a refused apply")
	}
}

func TestUpgradeStatusIdleByDefault(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v1.0.0","binaries":{"%s/%s":{"url":"u","sha256":"s"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	r := upgradeRouter(t, h)
	w := doJSON(r, "GET", "/api/admin/upgrade/status", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["phase"] != service.PhaseIdle {
		t.Errorf("phase: %v want idle", out["phase"])
	}
	if out["backup_available"] != false {
		t.Errorf("backup_available: %v want false", out["backup_available"])
	}
}

func TestUpgradeRollbackWithoutBackup(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v1.0.0","binaries":{"%s/%s":{"url":"u","sha256":"s"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	r := upgradeRouter(t, h)
	w := doJSON(r, "POST", "/api/admin/upgrade/rollback", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
}

// The container image sets DAYMUG_DISABLE_SELF_UPGRADE: every operation that
// would replace the binary refuses with a reason the panel can show, while
// the read-only status endpoint keeps working.
func TestUpgradeEndpointsRefuseWhenSelfUpgradeDisabled(t *testing.T) {
	t.Setenv(service.DisableSelfUpgradeEnv, "1")
	manifest := fmt.Sprintf(`{"version":"v2.0.0","binaries":{"%s/%s":{"url":"u","sha256":"s"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	h.Drainer = service.NewDrainer()
	r := upgradeRouter(t, h)

	for _, ep := range []struct{ method, path string }{
		{"GET", "/api/admin/upgrade/check"},
		{"POST", "/api/admin/upgrade/apply"},
		{"POST", "/api/admin/upgrade/rollback"},
	} {
		w := doJSON(r, ep.method, ep.path, nil)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: got %d want 503 body=%s", ep.method, ep.path, w.Code, w.Body.String())
			continue
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if out["error"] != service.ErrSelfUpgradeDisabled.Error() {
			t.Errorf("%s %s: error = %v", ep.method, ep.path, out["error"])
		}
	}
	if h.deferredUpgrade {
		t.Fatal("apply scheduled an upgrade while self-upgrade is disabled")
	}

	if w := doJSON(r, "GET", "/api/admin/upgrade/status", nil); w.Code != http.StatusOK {
		t.Fatalf("status endpoint: got %d want 200 body=%s", w.Code, w.Body.String())
	}
}

func TestUpgradeRestartSchedulesServiceRestart(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v1.0.0","binaries":{"%s/%s":{"url":"u","sha256":"s"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	h.Drainer = service.NewDrainer()

	called := make(chan [2]string, 1)
	oldRestart := restartService
	oldDelay := restartServiceDelay
	restartService = func(serviceName, mode string) ([]byte, error) {
		called <- [2]string{serviceName, mode}
		return nil, nil
	}
	restartServiceDelay = 0
	t.Cleanup(func() {
		restartService = oldRestart
		restartServiceDelay = oldDelay
	})

	r := upgradeRouter(t, h)
	w := doJSON(r, "POST", "/api/admin/upgrade/restart", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["status"] != "waiting_for_conversations" {
		t.Errorf("status: %v want waiting_for_conversations", out["status"])
	}
	select {
	case got := <-called:
		if got != [2]string{"daymug", "user"} {
			t.Fatalf("restart args: got %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("restart was not scheduled")
	}
}

func TestUpgradeRestartRequiresQuietWindow(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v1.0.0","binaries":{"%s/%s":{"url":"u","sha256":"s"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	h.Drainer = service.NewDrainer()
	h.gracefulDelay = 80 * time.Millisecond
	h.gracefulPoll = time.Millisecond

	called := make(chan struct{}, 1)
	oldRestart := restartService
	oldDelay := restartServiceDelay
	restartService = func(_, _ string) ([]byte, error) {
		called <- struct{}{}
		return nil, nil
	}
	restartServiceDelay = 0
	t.Cleanup(func() {
		restartService = oldRestart
		restartServiceDelay = oldDelay
	})

	r := upgradeRouter(t, h)
	if w := doJSON(r, "POST", "/api/admin/upgrade/restart", nil); w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	time.Sleep(20 * time.Millisecond)
	done, _, ok := h.Drainer.TryJobStartWithInfo(service.DrainerJob{Username: "alice"})
	if !ok {
		t.Fatal("request should be accepted during the quiet window")
	}
	done()

	select {
	case <-called:
		t.Fatal("restart ran before a full quiet window followed the request")
	case <-time.After(40 * time.Millisecond):
	}
	select {
	case <-called:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("restart did not run after the renewed quiet window")
	}
}

func TestUpgradeRestartWaitsWhileBusy(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v1.0.0","binaries":{"%s/%s":{"url":"u","sha256":"s"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	h.Drainer = service.NewDrainer()

	called := make(chan struct{}, 1)
	oldRestart := restartService
	oldDelay := restartServiceDelay
	restartService = func(_, _ string) ([]byte, error) {
		called <- struct{}{}
		return nil, nil
	}
	restartServiceDelay = 0
	t.Cleanup(func() {
		restartService = oldRestart
		restartServiceDelay = oldDelay
	})

	done := h.Drainer.JobStartWithInfo(service.DrainerJob{Username: "alice"})

	r := upgradeRouter(t, h)
	w := doJSON(r, "POST", "/api/admin/upgrade/restart", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["status"] != "waiting_for_conversations" {
		t.Errorf("status: %v want waiting_for_conversations", out["status"])
	}
	if out["drain_reason"] != string(service.DrainReasonRestart) {
		t.Errorf("drain_reason: %v want restart", out["drain_reason"])
	}
	if h.Drainer.IsDraining() {
		t.Fatal("restart should not drain while waiting for idle")
	}
	done2, _, ok := h.Drainer.TryJobStartWithInfo(service.DrainerJob{Username: "bob"})
	if !ok {
		t.Fatal("new jobs should be allowed while graceful restart waits")
	}
	select {
	case <-called:
		t.Fatal("restart ran before in-flight job finished")
	case <-time.After(25 * time.Millisecond):
	}
	done()
	done2()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("restart was not scheduled after drain")
	}
}

func TestUpgradeRestartScheduledRequestIsIdempotent(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v1.0.0","binaries":{"%s/%s":{"url":"u","sha256":"s"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	h.Drainer = service.NewDrainer()
	done := h.Drainer.JobStartWithInfo(service.DrainerJob{Username: "alice"})

	called := make(chan struct{}, 1)
	oldRestart := restartService
	oldDelay := restartServiceDelay
	restartService = func(_, _ string) ([]byte, error) {
		called <- struct{}{}
		return nil, nil
	}
	restartServiceDelay = 0
	t.Cleanup(func() {
		restartService = oldRestart
		restartServiceDelay = oldDelay
	})

	r := upgradeRouter(t, h)
	first := doJSON(r, "POST", "/api/admin/upgrade/restart", nil)
	second := doJSON(r, "POST", "/api/admin/upgrade/restart", nil)
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted {
		t.Fatalf("statuses: first=%d second=%d body=%s", first.Code, second.Code, second.Body.String())
	}
	done()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("restart was not scheduled")
	}
}

func TestUpgradeStatusReportsScheduledRestart(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v1.0.0","binaries":{"%s/%s":{"url":"u","sha256":"s"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	h.Drainer = service.NewDrainer()
	done := h.Drainer.JobStartWithInfo(service.DrainerJob{Username: "alice"})
	called := make(chan struct{}, 1)
	oldRestart := restartService
	oldDelay := restartServiceDelay
	restartService = func(_, _ string) ([]byte, error) {
		called <- struct{}{}
		return nil, nil
	}
	restartServiceDelay = 0
	t.Cleanup(func() {
		restartService = oldRestart
		restartServiceDelay = oldDelay
	})

	r := upgradeRouter(t, h)
	if w := doJSON(r, "POST", "/api/admin/upgrade/restart", nil); w.Code != http.StatusAccepted {
		t.Fatalf("restart status: got %d body=%s", w.Code, w.Body.String())
	}
	w := doJSON(r, "GET", "/api/admin/upgrade/status", nil)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["phase"] != "waiting_for_conversations" || out["pending_operation"] != "restart" {
		t.Fatalf("pending status: %v", out)
	}
	done()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("restart was not scheduled")
	}
}

func TestUpgradeApplyRefusesWhileValidating(t *testing.T) {
	// Build a real manifest that says we ARE behind, so Apply would
	// otherwise proceed all the way to spawning the watchdog (which we
	// don't want under test).
	bin := filepath.Join(t.TempDir(), "daymug")
	if err := os.WriteFile(bin, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-write a "validating" status so Apply trips the early-out.
	if err := service.WriteStatus(bin, service.UpgradeStatus{Phase: service.PhaseValidating}); err != nil {
		t.Fatal(err)
	}
	// Manifest server (won't be called because of the early-out, but the
	// upgrader still needs a valid base URL).
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	upgrader := service.NewUpgrader(&config.UpgradeConfig{}, "v1.0.0", ":8080")
	upgrader.ManifestURL = srv.URL
	h := &AdminUpgradeHandler{
		Upgrader: upgrader,
		BinPath:  bin,
	}
	r := upgradeRouter(t, h)
	w := doJSON(r, "POST", "/api/admin/upgrade/apply", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
}

// TestUpgradeApplySchedulesWhileBusy confirms that a graceful upgrade can be
// scheduled while jobs are active and still allows new jobs while it waits
// for the service to become idle.
func TestUpgradeApplySchedulesWhileBusy(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v2.0.0","binaries":{"%s/%s":{"url":"u","sha256":"s"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	h.Drainer = service.NewDrainer()
	done := h.Drainer.JobStartWithInfo(service.DrainerJob{Username: "alice"})

	r := upgradeRouter(t, h)
	w := doJSON(r, "POST", "/api/admin/upgrade/apply", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["status"] != "waiting_for_conversations" {
		t.Errorf("status: %v want waiting_for_conversations", out["status"])
	}
	if out["in_flight_jobs"].(float64) != 1 {
		t.Errorf("in_flight_jobs: %v want 1", out["in_flight_jobs"])
	}
	if h.Drainer.IsDraining() {
		t.Fatal("upgrade should not drain while waiting for idle")
	}
	done2, _, ok := h.Drainer.TryJobStartWithInfo(service.DrainerJob{Username: "bob"})
	if !ok {
		t.Fatal("new jobs should be allowed while graceful upgrade waits")
	}
	done()
	done2()
	waitUpgradePhase(t, h, service.PhaseFailed)
}

func TestUpgradeApplyRequiresQuietWindow(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v2.0.0","binaries":{"%s/%s":{"url":"u","sha256":"s"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	h.Drainer = service.NewDrainer()
	h.gracefulDelay = 80 * time.Millisecond
	h.gracefulPoll = time.Millisecond

	r := upgradeRouter(t, h)
	if w := doJSON(r, "POST", "/api/admin/upgrade/apply", nil); w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	time.Sleep(20 * time.Millisecond)
	done, _, ok := h.Drainer.TryJobStartWithInfo(service.DrainerJob{Username: "alice"})
	if !ok {
		t.Fatal("request should be accepted during the quiet window")
	}
	done()

	time.Sleep(40 * time.Millisecond)
	if h.Drainer.IsDraining() {
		t.Fatal("upgrade drained before a full quiet window followed the request")
	}
	waitUpgradePhase(t, h, service.PhaseFailed)
}

func TestUpgradeApplyScheduledRequestIsIdempotent(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v2.0.0","binaries":{"%s/%s":{"url":"u","sha256":"s"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	h.Drainer = service.NewDrainer()
	done := h.Drainer.JobStartWithInfo(service.DrainerJob{Username: "alice"})

	r := upgradeRouter(t, h)
	first := doJSON(r, "POST", "/api/admin/upgrade/apply", nil)
	second := doJSON(r, "POST", "/api/admin/upgrade/apply", nil)
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted {
		t.Fatalf("statuses: first=%d second=%d body=%s", first.Code, second.Code, second.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(second.Body.Bytes(), &out)
	if out["status"] != "waiting_for_conversations" || out["new_version"] != "v2.0.0" {
		t.Fatalf("duplicate response: %v", out)
	}

	done()
	waitUpgradePhase(t, h, service.PhaseFailed)
}

// TestUpgradeDeferredSkipWhileValidatingReleasesDrain is the regression for
// the "drainer locked forever" bug: a deferred upgrade that wakes up to find
// a previous upgrade still at phase=validating used to return without
// resetting the drain, so the server kept running while refusing every
// prompt until an operator restarted it by hand.
func TestUpgradeDeferredSkipWhileValidatingReleasesDrain(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v2.0.0","binaries":{"%s/%s":{"url":"u","sha256":"s"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, bin, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	h.Drainer = service.NewDrainer()
	done := h.Drainer.JobStartWithInfo(service.DrainerJob{Username: "alice"})

	r := upgradeRouter(t, h)
	if w := doJSON(r, "POST", "/api/admin/upgrade/apply", nil); w.Code != http.StatusAccepted {
		t.Fatalf("apply status: got %d body=%s", w.Code, w.Body.String())
	}

	// A leftover watchdog marker appears while the upgrade is still parked
	// behind alice's job — exactly the state a SIGKILLed watchdog leaves on
	// disk.
	if err := service.WriteStatus(bin, service.UpgradeStatus{Phase: service.PhaseValidating}); err != nil {
		t.Fatalf("seed validating status: %v", err)
	}
	done()

	// applyAfterIdle clears deferredUpgrade last, under h.mu, so observing
	// it false means every earlier statement (including the drain reset)
	// has already run.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		pending := h.deferredUpgrade
		h.mu.Unlock()
		if !pending {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got, err := service.ReadStatus(bin); err != nil || got.Phase != service.PhaseValidating {
		t.Fatalf("upgrade should have been skipped, not attempted: phase=%q err=%v", got.Phase, err)
	}
	if h.Drainer.IsDraining() {
		t.Fatal("drain gate still closed after the deferred upgrade was skipped")
	}
	if _, _, ok := h.Drainer.TryJobStartWithInfo(service.DrainerJob{Username: "bob"}); !ok {
		t.Fatal("new prompts still refused after the deferred upgrade was skipped")
	}
}

func TestUpgradeStatusReportsScheduledUpgrade(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v2.0.0","binaries":{"%s/%s":{"url":"u","sha256":"s"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	h.Drainer = service.NewDrainer()
	done := h.Drainer.JobStartWithInfo(service.DrainerJob{Username: "alice"})

	r := upgradeRouter(t, h)
	if w := doJSON(r, "POST", "/api/admin/upgrade/apply", nil); w.Code != http.StatusAccepted {
		t.Fatalf("apply status: got %d body=%s", w.Code, w.Body.String())
	}
	w := doJSON(r, "GET", "/api/admin/upgrade/status", nil)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["phase"] != "waiting_for_conversations" || out["pending_operation"] != "upgrade" {
		t.Fatalf("pending status: %v", out)
	}
	if out["new_version"] != "v2.0.0" {
		t.Fatalf("new_version: %v want v2.0.0", out["new_version"])
	}

	done()
	waitUpgradePhase(t, h, service.PhaseFailed)
}

// TestUpgradeApplyForceUpToDateDoesNotDrain confirms that the deferred
// upgrade path still short-circuits cleanly when the manifest matches the
// running version.
func TestUpgradeApplyForceUpToDateDoesNotDrain(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v1.0.0","binaries":{"%s/%s":{"url":"u","sha256":"%s"}}}`,
		runtime.GOOS, runtime.GOARCH, hex.EncodeToString(sha256.New().Sum(nil)))
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	h.Drainer = service.NewDrainer()
	h.Drainer.JobStart()
	defer h.Drainer.JobDone()

	r := upgradeRouter(t, h)
	w := doJSON(r, "POST", "/api/admin/upgrade/apply?force=1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["status"] != "up_to_date" {
		t.Errorf("status: %v want up_to_date", out["status"])
	}
	if h.Drainer.IsDraining() {
		t.Fatal("drainer should not start when there is no update")
	}
}

func TestUpgradeApplyForceSchedulesAfterBusyJobs(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v2.0.0","binaries":{"%s/%s":{"url":"u","sha256":"s"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, bin, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	h.Drainer = service.NewDrainer()
	done := h.Drainer.JobStartWithInfo(service.DrainerJob{Username: "alice"})

	r := upgradeRouter(t, h)
	w := doJSON(r, "POST", "/api/admin/upgrade/apply?force=1", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["status"] != "waiting_for_conversations" {
		t.Errorf("status: %v want waiting_for_conversations", out["status"])
	}
	if h.Drainer.IsDraining() {
		t.Fatal("drainer should not stop new prompt execution while waiting to upgrade")
	}
	got, _ := os.ReadFile(bin)
	if string(got) != "OLD" {
		t.Errorf("binary modified before in-flight job finished: %q", got)
	}
	done()
	waitUpgradePhase(t, h, service.PhaseFailed)
}

func TestUpgradeApplyDownloadFailureResetsDrain(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v2.0.0","binaries":{"%s/%s":{"url":"http://127.0.0.1:1/daymug","sha256":"deadbeef"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	h.Drainer = service.NewDrainer()

	r := upgradeRouter(t, h)
	w := doJSON(r, "POST", "/api/admin/upgrade/apply", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	waitUpgradePhase(t, h, service.PhaseFailed)
	if h.Drainer.IsDraining() {
		t.Fatal("drainer should reset when asynchronous upgrade apply fails")
	}
}

func TestUpgradeApplyForceCheckFailureAfterDrainResetsDrain(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v2.0.0","binaries":{"%s/%s":{"url":"http://127.0.0.1:1/daymug","sha256":"deadbeef"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, _, srv := stubbedHandler(t, manifest)
	h.Drainer = service.NewDrainer()
	done := h.Drainer.JobStartWithInfo(service.DrainerJob{Username: "alice"})

	r := upgradeRouter(t, h)
	w := doJSON(r, "POST", "/api/admin/upgrade/apply?force=1", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	srv.Close()
	done()

	waitUpgradePhase(t, h, service.PhaseFailed)
	if h.Drainer.IsDraining() {
		t.Fatal("drainer should reset after deferred upgrade check fails")
	}
}

// TestUpgradeCheckIncludesInFlightJobs exercises the advisory field the
// admin UI uses to disable the Update button before the user clicks it.
// We don't assert on the exact phase mechanics here — just that the gate
// signal is exposed alongside the version metadata.
func TestUpgradeCheckIncludesInFlightJobs(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v2.0.0","binaries":{"%s/%s":{"url":"u","sha256":"s"}}}`,
		runtime.GOOS, runtime.GOARCH)
	h, _, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	h.Drainer = service.NewDrainer()
	doneAlice := h.Drainer.JobStartWithInfo(service.DrainerJob{
		UserID:       "u1",
		Username:     "alice",
		ProviderType: "codex",
		AccountName:  "codex-main",
	})
	doneBob := h.Drainer.JobStartWithInfo(service.DrainerJob{
		UserID:       "u2",
		Username:     "bob",
		ProviderType: "claude",
		AccountName:  "claude-main",
	})
	defer func() { doneAlice(); doneBob() }()

	r := upgradeRouter(t, h)
	w := doJSON(r, "GET", "/api/admin/upgrade/check", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["in_flight_jobs"].(float64) != 2 {
		t.Errorf("in_flight_jobs: %v want 2", out["in_flight_jobs"])
	}
	jobs := out["jobs"].([]any)
	if len(jobs) != 2 {
		t.Fatalf("jobs len = %d, want 2", len(jobs))
	}
	first := jobs[0].(map[string]any)
	if first["username"] != "alice" || first["provider_type"] != "codex" || first["account_name"] != "codex-main" {
		t.Errorf("first job = %+v", first)
	}
}

// TestUpgradeCheckSurfacesRecentReleases verifies the admin /check
// response carries through the manifest's recent_releases field so the
// admin UI can render the changelog for the last ~5 tags. Confirms both
// presence and the empty-array fallback on manifests without the field.
func TestUpgradeCheckSurfacesRecentReleases(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		manifest := fmt.Sprintf(`{
  "version": "v2.0.0",
  "binaries": {"%s/%s": {"url": "u", "sha256": "s"}},
  "recent_releases": [
    {"version": "v2.0.0", "released_at": "2026-05-22T00:00:00Z", "notes": [
      {"hash": "abc1234", "subject": "feat: panel"}
    ]},
    {"version": "v1.9.0", "released_at": "2026-04-30T00:00:00Z", "notes": []}
  ]
}`, runtime.GOOS, runtime.GOARCH)
		h, _, srv := stubbedHandler(t, manifest)
		defer srv.Close()
		r := upgradeRouter(t, h)
		w := doJSON(r, "GET", "/api/admin/upgrade/check", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		releases, ok := out["recent_releases"].([]any)
		if !ok {
			t.Fatalf("recent_releases: %T %v", out["recent_releases"], out["recent_releases"])
		}
		if len(releases) != 2 {
			t.Fatalf("recent_releases length: got %d want 2", len(releases))
		}
		first := releases[0].(map[string]any)
		if first["version"] != "v2.0.0" {
			t.Errorf("first version: %v", first["version"])
		}
		notes := first["notes"].([]any)
		if len(notes) != 1 || notes[0].(map[string]any)["subject"] != "feat: panel" {
			t.Errorf("first notes: %v", notes)
		}
	})

	t.Run("legacy manifest yields empty array", func(t *testing.T) {
		manifest := fmt.Sprintf(`{"version":"v2.0.0","binaries":{"%s/%s":{"url":"u","sha256":"s"}}}`,
			runtime.GOOS, runtime.GOARCH)
		h, _, srv := stubbedHandler(t, manifest)
		defer srv.Close()
		r := upgradeRouter(t, h)
		w := doJSON(r, "GET", "/api/admin/upgrade/check", nil)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		releases, ok := out["recent_releases"].([]any)
		if !ok {
			t.Fatalf("recent_releases missing or wrong type: %T", out["recent_releases"])
		}
		if len(releases) != 0 {
			t.Errorf("expected empty array, got %v", releases)
		}
	})
}

// TestUpgradeBusyEndpoint covers the polling endpoint the admin panel
// uses to react when users finish their prompts without re-running Check.
func TestUpgradeBusyEndpoint(t *testing.T) {
	h := &AdminUpgradeHandler{Drainer: service.NewDrainer()}
	r := gin.New()
	r.GET("/api/admin/upgrade/busy", h.Busy)

	w := doJSON(r, "GET", "/api/admin/upgrade/busy", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("idle status: got %d", w.Code)
	}
	var idle map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &idle)
	if idle["in_flight_jobs"].(float64) != 0 {
		t.Errorf("idle in_flight_jobs: %v want 0", idle["in_flight_jobs"])
	}

	done := h.Drainer.JobStartWithInfo(service.DrainerJob{
		UserID:       "u1",
		Username:     "alice",
		ProviderType: "codex",
		AccountName:  "codex-main",
	})
	defer done()
	w = doJSON(r, "GET", "/api/admin/upgrade/busy", nil)
	var busy map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &busy)
	if busy["in_flight_jobs"].(float64) != 1 {
		t.Errorf("busy in_flight_jobs: %v want 1", busy["in_flight_jobs"])
	}
	jobs := busy["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("busy jobs len = %d, want 1", len(jobs))
	}
	job := jobs[0].(map[string]any)
	if job["username"] != "alice" || job["account_name"] != "codex-main" {
		t.Errorf("busy job = %+v", job)
	}
}

// TestUpgradeBusyListsResidentBackgroundWork keeps the banner honest about
// work a graceful restart waits for without it being an in-flight job.
func TestUpgradeBusyListsResidentBackgroundWork(t *testing.T) {
	h := &AdminUpgradeHandler{Drainer: service.NewDrainer()}
	r := gin.New()
	r.GET("/api/admin/upgrade/busy", h.Busy)
	h.Drainer.SetResidentActivity("bridge-1", service.DrainerJob{
		Username:       "alice",
		ConversationID: "c1",
	}, true)

	w := doJSON(r, "GET", "/api/admin/upgrade/busy", nil)
	var busy map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &busy)
	if busy["in_flight_jobs"].(float64) != 0 {
		t.Errorf("in_flight_jobs = %v, want 0: background work is not a job", busy["in_flight_jobs"])
	}
	jobs := busy["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(jobs))
	}
	if job := jobs[0].(map[string]any); job["username"] != "alice" || job["status"] != service.JobStatusBackground {
		t.Errorf("job = %+v, want alice as background", job)
	}
}

// TestUpgradeApplyAlreadyUpToDate exercises the Apply path through Check
// and confirms that when the manifest matches the current version we
// return 200 with status=up_to_date instead of swapping anything.
func TestUpgradeApplyAlreadyUpToDate(t *testing.T) {
	manifest := fmt.Sprintf(`{"version":"v1.0.0","binaries":{"%s/%s":{"url":"u","sha256":"%s"}}}`,
		runtime.GOOS, runtime.GOARCH, hex.EncodeToString(sha256.New().Sum(nil)))
	h, bin, srv := stubbedHandler(t, manifest)
	defer srv.Close()
	r := upgradeRouter(t, h)
	w := doJSON(r, "POST", "/api/admin/upgrade/apply", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", w.Code, w.Body.String())
	}
	// Active binary must still be the original "OLD" — no swap happened.
	got, _ := os.ReadFile(bin)
	if string(got) != "OLD" {
		t.Errorf("binary unexpectedly modified: %q", got)
	}
	// And no .bak should exist.
	if _, err := os.Stat(service.BackupPath(bin)); !os.IsNotExist(err) {
		t.Errorf("backup unexpectedly created: %v", err)
	}
}

// A config the incoming release rejects must stop the upgrade before the
// swap: the installed binary stays, and no staged download is left behind.
func TestApplyCheckedUpdateAbortsWhenPreflightRejectsConfig(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "daymug")
	if err := os.WriteFile(binPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("new-binary")
	sum := sha256.Sum256(payload)

	up := service.NewUpgrader(&config.UpgradeConfig{}, "v1.0.0", "127.0.0.1:8090")
	up.ConfigPath = filepath.Join(dir, "config.yaml")
	up.HTTPGet = func(context.Context, string) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(payload))}, nil
	}
	h := &AdminUpgradeHandler{Upgrader: up, BinPath: binPath}

	var gotConfig string
	oldPreflight := preflightConfig
	preflightConfig = func(_ context.Context, _ string, configPath string) error {
		gotConfig = configPath
		return fmt.Errorf("new release rejects %s", configPath)
	}
	t.Cleanup(func() { preflightConfig = oldPreflight })

	res, err := h.applyCheckedUpdate("v1.1.0", &service.ManifestEntry{URL: "https://example.test/daymug", SHA256: hex.EncodeToString(sum[:])})
	if err == nil {
		t.Fatal("expected preflight error")
	}
	if res.statusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", res.statusCode)
	}
	if gotConfig != up.ConfigPath {
		t.Fatalf("preflight got config %q, want %q", gotConfig, up.ConfigPath)
	}
	if got, _ := os.ReadFile(binPath); string(got) != "old-binary" {
		t.Fatalf("installed binary changed: %q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files after aborted upgrade: %v", entries)
	}
}
