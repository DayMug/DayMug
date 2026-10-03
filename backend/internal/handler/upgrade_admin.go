package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/service"
)

// AdminUpgradeHandler exposes the /api/admin/upgrade/* endpoints. It
// is split out from AdminHandler so the upgrader injection (and the
// serializing mutex) stay in one file.
//
// Concurrency: only one apply / rollback may run at a time. The mutex is
// taken before any disk mutation; the actual `systemctl restart` happens
// inside the watchdog (a detached process) so we release the mutex right
// after spawning it. The next admin request sees status=validating from
// the persisted state file and refuses to apply again.
type AdminUpgradeHandler struct {
	Upgrader *service.Upgrader
	BinPath  string
	// Drainer is the same shared instance the WebSocket handler uses to
	// bracket every claude/codex CLI invocation. Graceful upgrade/restart
	// requests wait for a quiet window, then atomically start draining, so
	// they cannot cut off an active reply or race a newly submitted prompt. Nil-safe:
	// a nil drainer reports zero in-flight jobs, which keeps unit tests
	// that don't wire one up working.
	Drainer         *service.Drainer
	gracefulDelay   time.Duration
	gracefulPoll    time.Duration
	mu              sync.Mutex
	deferredUpgrade bool
	deferredRestart bool
	deferredVersion string
}

var (
	preflightConfig     = service.PreflightConfig
	restartService      = service.RestartService
	restartServiceDelay = 250 * time.Millisecond
)

const (
	defaultGracefulDelay = 3 * time.Second
	defaultGracefulPoll  = 100 * time.Millisecond
)

// NewAdminUpgradeHandler resolves the binary path eagerly so that any
// permission / symlink error surfaces at startup rather than on the first
// admin click. A nil upgrader makes every upgrade operation return 503.
func NewAdminUpgradeHandler(upgrader *service.Upgrader, drainer *service.Drainer) *AdminUpgradeHandler {
	h := &AdminUpgradeHandler{
		Upgrader:      upgrader,
		Drainer:       drainer,
		gracefulDelay: defaultGracefulDelay,
		gracefulPoll:  defaultGracefulPoll,
	}
	if upgrader == nil {
		return h
	}
	binPath, err := os.Executable()
	if err == nil {
		if resolved, e2 := filepath.EvalSymlinks(binPath); e2 == nil {
			binPath = resolved
		}
	}
	h.BinPath = binPath
	return h
}

// Check fetches the manifest and reports whether the current binary is
// behind. Safe to call repeatedly — no disk side-effects.
func (h *AdminUpgradeHandler) Check(c *gin.Context) {
	if !h.configured(c) || !selfUpgradeAllowed(c) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	manifest, entry, err := h.Upgrader.Check(ctx)
	// A release with no artifact for this platform is a normal outcome,
	// not a fetch failure — report it as a 200 the panel can explain.
	if err != nil && !errors.Is(err, service.ErrNoPlatformBuild) {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	downloadURL := ""
	if entry != nil {
		downloadURL = entry.URL
	}
	c.JSON(http.StatusOK, gin.H{
		"current_version": h.Upgrader.CurrentVersion,
		"latest_version":  manifest.Version,
		"released_at":     manifest.ReleasedAt,
		// Without an artifact there is nothing to apply, so this stays
		// false even when the manifest advertises a newer version —
		// platform_supported is what tells the two cases apart.
		"has_update":         entry != nil && h.Upgrader.HasUpdate(manifest),
		"platform_supported": entry != nil,
		"download_url":       downloadURL,
		// Surface the live in-flight job count so the admin UI can warn
		// before the user clicks Update. Apply re-checks this under the
		// upgrade mutex; the value here is advisory.
		"in_flight_jobs": h.Drainer.InFlight(),
		"jobs":           h.busyJobs(),
		// Last 5 tags with commit subjects (chore/docs/ci pre-filtered).
		// Empty array on older manifests that predate this field; nil
		// would marshal as null, which the frontend would have to guard.
		"recent_releases": nonNilReleases(manifest.RecentReleases),
	})
}

// nonNilReleases keeps the JSON output an array even when the manifest
// is missing the field — easier for the frontend to consume.
func nonNilReleases(r []service.ManifestRelease) []service.ManifestRelease {
	if r == nil {
		return []service.ManifestRelease{}
	}
	return r
}

// busyJobs lists what a graceful restart waits on: in-flight jobs plus
// resident background work, which counts toward no job yet still defers an
// upgrade or restart until its agent process goes quiet.
func (h *AdminUpgradeHandler) busyJobs() []service.DrainerJob {
	jobs := h.Drainer.Jobs()
	for _, job := range h.Drainer.ResidentActivities() {
		job.Status = service.JobStatusBackground
		jobs = append(jobs, job)
	}
	return jobs
}

// Busy returns the current in-flight job count. Lightweight enough that
// the admin upgrade panel can poll it while the page is open so the
// "running conversations" banner reacts as users finish their prompts.
func (h *AdminUpgradeHandler) Busy(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"in_flight_jobs": h.Drainer.InFlight(), "jobs": h.busyJobs()})
}

// Restart schedules a service-manager restart after a three-second quiet
// window. New prompt work is still allowed while waiting and restarts the
// window; the background worker starts draining atomically with the final
// activity check to close the race before the restart command runs.
func (h *AdminUpgradeHandler) Restart(c *gin.Context) {
	if !h.configured(c) {
		return
	}
	if !h.mu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "another upgrade or restart is already in progress"})
		return
	}
	defer h.mu.Unlock()

	if h.deferredUpgrade {
		c.JSON(http.StatusConflict, gin.H{"error": "upgrade already scheduled after conversations finish"})
		return
	}
	if h.deferredRestart {
		n := h.Drainer.InFlight()
		c.JSON(http.StatusAccepted, gin.H{
			"status":          "waiting_for_conversations",
			"in_flight_jobs":  n,
			"jobs":            h.busyJobs(),
			"drain_reason":    service.DrainReasonRestart,
			"watchdog_active": false,
		})
		return
	}
	if h.Drainer.IsDraining() {
		c.JSON(http.StatusConflict, gin.H{"error": "service is already draining"})
		return
	}

	serviceName := h.Upgrader.Cfg.Service
	serviceMode := h.Upgrader.Cfg.ServiceMode
	n := h.Drainer.InFlight()
	h.deferredRestart = true
	go h.restartAfterIdle(serviceName, serviceMode)

	c.JSON(http.StatusAccepted, gin.H{
		"status":          "waiting_for_conversations",
		"in_flight_jobs":  n,
		"jobs":            h.busyJobs(),
		"drain_reason":    service.DrainReasonRestart,
		"watchdog_active": false,
	})
}

// Apply runs the full upgrade flow: download + verify + atomic swap +
// spawn watchdog. Returns 200 once the watchdog is running; the client
// then polls Status to see the outcome.
//
// Implementation note: we don't restart the service from this handler —
// the watchdog does. That way the HTTP response can be flushed before
// the running process is killed.
func (h *AdminUpgradeHandler) Apply(c *gin.Context) {
	if !h.configured(c) || !selfUpgradeAllowed(c) {
		return
	}
	if !h.mu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "another upgrade is already in progress"})
		return
	}
	defer h.mu.Unlock()

	// Refuse a second apply if the previous one is still validating.
	if prev, err := service.ReadStatus(h.BinPath); err == nil && prev.Phase == service.PhaseValidating {
		c.JSON(http.StatusConflict, gin.H{"error": "previous upgrade is still validating; wait for the watchdog or roll back"})
		return
	}
	if h.deferredUpgrade {
		c.JSON(http.StatusAccepted, gin.H{
			"status":          "waiting_for_conversations",
			"old_version":     h.Upgrader.CurrentVersion,
			"new_version":     h.deferredVersion,
			"watchdog_active": false,
			"in_flight_jobs":  h.Drainer.InFlight(),
			"jobs":            h.busyJobs(),
		})
		return
	}
	if h.deferredRestart {
		c.JSON(http.StatusConflict, gin.H{"error": "restart already scheduled after conversations finish"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	manifest, _, err := h.Upgrader.Check(ctx)
	if err != nil {
		// Nothing to download for this platform: refuse up front rather
		// than draining every conversation for an upgrade that cannot run.
		if errors.Is(err, service.ErrNoPlatformBuild) {
			c.JSON(http.StatusOK, gin.H{"status": "no_platform_build", "version": manifest.Version})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if !h.Upgrader.HasUpdate(manifest) {
		c.JSON(http.StatusOK, gin.H{"status": "up_to_date", "version": manifest.Version})
		return
	}

	h.deferredUpgrade = true
	h.deferredVersion = manifest.Version
	go h.applyAfterIdle()
	c.JSON(http.StatusAccepted, gin.H{
		"status":          "waiting_for_conversations",
		"old_version":     h.Upgrader.CurrentVersion,
		"new_version":     manifest.Version,
		"watchdog_active": false,
		"in_flight_jobs":  h.Drainer.InFlight(),
		"jobs":            h.busyJobs(),
	})
}

type applyUpdateResult struct {
	statusCode int
	body       gin.H
}

func (h *AdminUpgradeHandler) applyCheckedUpdate(newVersion string, entry *service.ManifestEntry) (applyUpdateResult, error) {
	dlCtx, dlCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer dlCancel()
	staged, err := h.Upgrader.DownloadAndVerify(dlCtx, entry, h.BinPath)
	if err != nil {
		return applyUpdateResult{statusCode: http.StatusBadGateway}, err
	}

	// Validate the config with the incoming release before touching the
	// installed binary: a config it rejects would only surface as a failed
	// start, a rollback, and downtime in between.
	if err := preflightConfig(dlCtx, staged, h.Upgrader.ConfigPath); err != nil {
		_ = os.Remove(staged)
		return applyUpdateResult{statusCode: http.StatusUnprocessableEntity}, err
	}

	bak, err := service.SwapInPlace(h.BinPath, staged)
	if err != nil {
		_ = os.Remove(staged)
		return applyUpdateResult{statusCode: http.StatusInternalServerError}, err
	}

	_ = service.WriteStatus(h.BinPath, service.UpgradeStatus{
		Phase:      service.PhaseValidating,
		OldVersion: h.Upgrader.CurrentVersion,
		NewVersion: newVersion,
		StartedAt:  time.Now().UTC(),
	})

	if err := service.SpawnWatchdog(
		bak, h.BinPath,
		h.Upgrader.HealthURL(),
		h.Upgrader.CurrentVersion, newVersion,
		h.Upgrader.Cfg.Service, h.Upgrader.Cfg.ServiceMode,
		h.Upgrader.Cfg.HealthTimeout.Duration,
	); err != nil {
		// Watchdog failed to spawn. Best-effort restore and report.
		if rerr := service.RestoreBackup(h.BinPath); rerr != nil {
			return applyUpdateResult{statusCode: http.StatusInternalServerError}, errors.New("watchdog spawn failed AND rollback failed: " + err.Error() + "; " + rerr.Error())
		}
		return applyUpdateResult{statusCode: http.StatusInternalServerError}, errors.New("watchdog spawn failed: " + err.Error())
	}

	return applyUpdateResult{statusCode: http.StatusOK, body: gin.H{
		"status":          "validating",
		"old_version":     h.Upgrader.CurrentVersion,
		"new_version":     newVersion,
		"watchdog_active": true,
	}}, nil
}

func (h *AdminUpgradeHandler) applyAfterIdle() {
	h.waitForIdleThenDrain(service.DrainReasonUpgrade)
	h.mu.Lock()
	defer h.mu.Unlock()
	defer func() {
		h.deferredUpgrade = false
		h.deferredVersion = ""
	}()

	// 上一次升级还停在 validating：可能看门狗真的在飞，也可能它被 SIGKILL 掉、
	// 状态文件成了永久残留。两种情况都不该再叠加一次升级——但 drain 是这个
	// goroutine 自己开的，直接 return 就没有人复位它，服务会继续运行却永久
	// 拒绝所有 prompt，只能人工重启。和下面两条失败路径一样先 ResetDrain：
	// 真正要重启的那次升级由看门狗负责，它会重新把服务停掉。
	if prev, err := service.ReadStatus(h.BinPath); err == nil && prev.Phase == service.PhaseValidating {
		log.Printf("deferred upgrade skipped: a previous upgrade is still in %q phase; releasing the drain gate", prev.Phase)
		h.Drainer.ResetDrain()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	manifest, entry, err := h.Upgrader.Check(ctx)
	if err != nil || !h.Upgrader.HasUpdate(manifest) {
		if err != nil {
			log.Printf("deferred upgrade check failed: %v", err)
			h.writeUpgradeFailure(err)
		}
		h.Drainer.ResetDrain()
		return
	}
	if _, err := h.applyCheckedUpdate(manifest.Version, entry); err != nil {
		log.Printf("deferred upgrade apply failed: %v", err)
		h.writeUpgradeFailure(err)
		h.Drainer.ResetDrain()
	}
}

func (h *AdminUpgradeHandler) restartAfterIdle(serviceName, serviceMode string) {
	h.waitForIdleThenDrain(service.DrainReasonRestart)
	if restartServiceDelay > 0 {
		time.Sleep(restartServiceDelay)
	}
	if out, err := restartService(serviceName, serviceMode); err != nil {
		log.Printf("graceful restart failed: %v: %s", err, out)
		h.Drainer.ResetDrain()
		h.mu.Lock()
		h.deferredRestart = false
		h.mu.Unlock()
	}
}

func (h *AdminUpgradeHandler) waitForIdleThenDrain(reason service.DrainReason) {
	delay := h.gracefulDelay
	poll := h.gracefulPoll
	if poll <= 0 {
		poll = defaultGracefulPoll
	}
	activityID := h.Drainer.ActivityID()
	idleSince := time.Now()
	for {
		currentActivityID := h.Drainer.ActivityID()
		if currentActivityID != activityID || h.Drainer.Busy() != 0 {
			activityID = currentActivityID
			idleSince = time.Now()
		} else if time.Since(idleSince) >= delay &&
			h.Drainer.TryStartDrainWhenIdleSince(reason, activityID) {
			return
		}
		time.Sleep(poll)
	}
}

func (h *AdminUpgradeHandler) writeUpgradeFailure(err error) {
	if err == nil {
		return
	}
	_ = service.WriteStatus(h.BinPath, service.UpgradeStatus{
		Phase:      service.PhaseFailed,
		OldVersion: h.Upgrader.CurrentVersion,
		FinishedAt: time.Now().UTC(),
		Error:      err.Error(),
	})
}

// Rollback restores <bin>.bak over <bin> and restarts the service.
// Used as the manual escape hatch when the watchdog couldn't auto-rollback
// (rare) or when the operator wants to revert a healthy-but-buggy build.
func (h *AdminUpgradeHandler) Rollback(c *gin.Context) {
	if !h.configured(c) || !selfUpgradeAllowed(c) {
		return
	}
	if !h.mu.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "another upgrade or rollback is in progress"})
		return
	}
	defer h.mu.Unlock()

	if _, err := os.Stat(service.BackupPath(h.BinPath)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no backup binary at " + service.BackupPath(h.BinPath)})
			return
		}
		respondInternalError(c, "AdminUpgradeHandler.Rollback", err)
		return
	}
	if err := service.RestoreBackup(h.BinPath); err != nil {
		respondInternalError(c, "AdminUpgradeHandler.Rollback", err)
		return
	}
	prev, _ := service.ReadStatus(h.BinPath)
	_ = service.WriteStatus(h.BinPath, service.UpgradeStatus{
		Phase:      service.PhaseRolledBack,
		OldVersion: prev.NewVersion,
		NewVersion: prev.OldVersion,
		FinishedAt: time.Now().UTC(),
		Error:      "manual rollback",
	})
	if out, err := service.RestartService(h.Upgrader.Cfg.Service, h.Upgrader.Cfg.ServiceMode); err != nil {
		log.Printf("[AdminUpgradeHandler.Rollback] restart after restore: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  "rollback restored binary but restart failed",
			"output": string(out),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "rolled_back"})
}

// Status reads <bin>.upgrade-state.json and returns the persisted phase.
// Polled by the admin UI while an apply is in flight.
func (h *AdminUpgradeHandler) Status(c *gin.Context) {
	if !h.configured(c) {
		return
	}
	s, err := service.ReadStatus(h.BinPath)
	if err != nil {
		respondInternalError(c, "AdminUpgradeHandler.Status", err)
		return
	}
	_, bakErr := os.Stat(service.BackupPath(h.BinPath))
	h.mu.Lock()
	deferredUpgrade := h.deferredUpgrade
	deferredRestart := h.deferredRestart
	deferredVersion := h.deferredVersion
	h.mu.Unlock()
	phase := s.Phase
	pendingOperation := ""
	if deferredUpgrade || deferredRestart {
		phase = "waiting_for_conversations"
	}
	if deferredUpgrade {
		pendingOperation = "upgrade"
		if s.NewVersion == "" {
			s.NewVersion = deferredVersion
		}
	} else if deferredRestart {
		pendingOperation = "restart"
	}
	c.JSON(http.StatusOK, gin.H{
		"phase":             phase,
		"old_version":       s.OldVersion,
		"new_version":       s.NewVersion,
		"started_at":        s.StartedAt,
		"finished_at":       s.FinishedAt,
		"error":             s.Error,
		"current_version":   h.Upgrader.CurrentVersion,
		"backup_available":  bakErr == nil,
		"draining":          h.Drainer.IsDraining(),
		"drain_reason":      h.Drainer.Reason(),
		"in_flight_jobs":    h.Drainer.InFlight(),
		"jobs":              h.busyJobs(),
		"pending_operation": pendingOperation,
	})
}

// configured returns true if the handler has an upgrader wired up. The
// upgrade feature is now always-on (manifest URL is hard-coded in
// service.DefaultManifestURL); this guard only protects against the
// upgrader being nil in unit tests.
func (h *AdminUpgradeHandler) configured(c *gin.Context) bool {
	if h == nil || h.Upgrader == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": service.ErrUpgradeNotConfigured.Error()})
		return false
	}
	return true
}

// selfUpgradeAllowed refuses the operations that would touch the installed
// binary when the operator turned self-upgrade off (the container image
// does). 503 carries the reason, which the admin panel shows verbatim;
// Status and Busy stay available since they only read.
func selfUpgradeAllowed(c *gin.Context) bool {
	if service.SelfUpgradeDisabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": service.ErrSelfUpgradeDisabled.Error()})
		return false
	}
	return true
}
