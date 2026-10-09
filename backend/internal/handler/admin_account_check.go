package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
)

// AdminAccountCheckHandler answers "can this provider account actually run
// chat right now?" from the models page, so an admin configuring, logging in
// and verifying an account never has to leave it. The check has two stages:
//
//  1. Auth: ask the account's own CLI whether it holds credentials
//     (`claude auth status`, `codex login status`) under the account's config
//     dir. Cheap, and it names the login method, which the next stage can't.
//  2. Run: one tiny read-only turn through the backend chat would use — the
//     admin-selected transport, the account's env and its default model. This
//     is what catches an expired token, a model id the account can't serve, or
//     a transport runtime that went missing, none of which stage 1 sees.
type AdminAccountCheckHandler struct {
	Cfg      *config.Config
	Backends *service.BackendRegistry
	// Pool caps live processes per account; the run stage takes a slot like
	// the admin terminal does, so repeated checks can't fan out past the
	// account's limits. Nil disables the cap (tests).
	Pool *service.Pool
	// AuthStatus runs the stage-1 command. Swappable for tests.
	AuthStatus func(ctx context.Context, acc *config.Provider) accountAuthStatus
	// RunTimeout bounds the stage-2 turn. Zero means accountCheckRunTimeout.
	RunTimeout time.Duration

	mu       sync.Mutex
	inFlight map[string]bool
}

const (
	accountCheckAuthTimeout = 20 * time.Second
	accountCheckRunTimeout  = 2 * time.Minute
	accountCheckPrompt      = "This is a connectivity check. Reply with exactly: OK"
	accountCheckReplyRunes  = 200
)

func NewAdminAccountCheckHandler(cfg *config.Config, backends *service.BackendRegistry, pool *service.Pool) *AdminAccountCheckHandler {
	return &AdminAccountCheckHandler{Cfg: cfg, Backends: backends, Pool: pool, AuthStatus: runAuthStatus}
}

// accountAuthStatus is stage 1. Checked is false for the compatible types:
// they authenticate with an API key from the account's env, which has no
// status command — stage 2 is the only meaningful test for them.
type accountAuthStatus struct {
	Checked  bool   `json:"checked"`
	LoggedIn bool   `json:"logged_in"`
	Method   string `json:"method,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Error    string `json:"error,omitempty"`
}

type accountRunResult struct {
	Attempted bool   `json:"attempted"`
	OK        bool   `json:"ok"`
	Model     string `json:"model,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Reply     string `json:"reply,omitempty"`
	Error     string `json:"error,omitempty"`
}

type accountCheckResponse struct {
	Account   string            `json:"account"`
	Provider  string            `json:"provider"`
	Transport string            `json:"transport"`
	OK        bool              `json:"ok"`
	Auth      accountAuthStatus `json:"auth"`
	Run       accountRunResult  `json:"run"`
	CheckedAt time.Time         `json:"checked_at"`
}

// Check serves POST /api/admin/providers/:name/check.
func (h *AdminAccountCheckHandler) Check(c *gin.Context) {
	name := c.Param("name")
	var acc *config.Provider
	if h.Cfg != nil {
		acc = h.Cfg.FindAccount(name)
	}
	if acc == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown account " + name})
		return
	}
	if !h.begin(acc.Name) {
		c.JSON(http.StatusConflict, gin.H{"error": "a check for account " + acc.Name + " is already running"})
		return
	}
	defer h.end(acc.Name)

	ctx := c.Request.Context()
	resp := accountCheckResponse{
		Account:   acc.Name,
		Provider:  acc.Type,
		Transport: service.TransportFor(acc.Type),
		CheckedAt: time.Now().UTC(),
	}
	if h.AuthStatus != nil {
		authCtx, cancel := context.WithTimeout(ctx, accountCheckAuthTimeout)
		resp.Auth = h.AuthStatus(authCtx, acc)
		cancel()
	}
	// A CLI that says it holds no credentials would only fail the run with a
	// less useful message, so skip straight to "log in first".
	if !resp.Auth.Checked || resp.Auth.LoggedIn {
		resp.Run = h.run(ctx, acc)
	}
	resp.OK = resp.Run.OK
	c.JSON(http.StatusOK, resp)
}

func (h *AdminAccountCheckHandler) begin(account string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.inFlight == nil {
		h.inFlight = map[string]bool{}
	}
	if h.inFlight[account] {
		return false
	}
	h.inFlight[account] = true
	return true
}

func (h *AdminAccountCheckHandler) end(account string) {
	h.mu.Lock()
	delete(h.inFlight, account)
	h.mu.Unlock()
}

// run is stage 2: one read-only turn in a throwaway directory, on the
// account's default model — the one new conversations use.
func (h *AdminAccountCheckHandler) run(ctx context.Context, acc *config.Provider) accountRunResult {
	out := accountRunResult{Attempted: true, Model: service.LatestModelForAccount(h.Cfg, acc.Type, acc.Name)}
	registered, _ := h.Backends.Lookup(acc.Type)
	backend := agent.Resolve(registered)
	if backend == nil {
		out.Error = "no backend registered for provider type " + acc.Type
		return out
	}
	if h.Pool != nil {
		release, err := h.Pool.EnterLive(acc.Name)
		if err != nil {
			out.Error = err.Error()
			return out
		}
		defer release()
	}
	workDir, err := os.MkdirTemp("", "daymug-account-check-")
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	timeout := h.RunTimeout
	if timeout <= 0 {
		timeout = accountCheckRunTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req := agent.RunRequest{
		SessionID:  uuid.NewString(),
		Model:      out.Model,
		ReadOnly:   true,
		ConfigDir:  acc.ConfigDir,
		AccountEnv: acc.Env,
	}
	start := time.Now()
	reply, err := backend.RunOneshot(runCtx, accountCheckPrompt, workDir, req)
	out.LatencyMS = time.Since(start).Milliseconds()
	removeCheckSessionLog(backend, workDir, req.SessionID, acc.ConfigDir)
	reply = strings.TrimSpace(reply)
	switch {
	case err != nil:
		out.Error = err.Error()
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			out.Error = "no reply within " + timeout.String() + ": " + out.Error
		}
	case reply == "":
		out.Error = "the model returned an empty reply"
	default:
		out.OK = true
	}
	out.Reply = truncateRunes(reply, accountCheckReplyRunes)
	return out
}

// removeCheckSessionLog deletes the session the check created so probing an
// account doesn't leave a stray conversation in its history. Best effort: a
// transport that can't predict the path leaves it behind.
func removeCheckSessionLog(backend agent.Backend, workDir, sessionID, configDir string) {
	path := backend.SessionLogPath(workDir, sessionID, configDir)
	if path == "" {
		return
	}
	if err := os.Remove(path); err == nil {
		// Claude keys the directory by cwd, so it held only this session.
		_ = os.Remove(filepath.Dir(path))
	}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// runAuthStatus asks the account's CLI for its login state. Only the
// first-party types have one; the compatible types report Checked=false.
func runAuthStatus(ctx context.Context, acc *config.Provider) accountAuthStatus {
	switch acc.Type {
	case config.CLITypeClaude:
		out, err := runAccountCommand(ctx, acc, "CLAUDE_CONFIG_DIR",
			agent.ResolveAgentBinary("claude"), "auth", "status", "--json")
		return parseClaudeAuthStatus(out, err)
	case config.CLITypeCodex:
		out, err := runAccountCommand(ctx, acc, "CODEX_HOME",
			agent.ResolveAgentBinary("codex"), "login", "status")
		return parseCodexLoginStatus(out, err)
	}
	return accountAuthStatus{}
}

func runAccountCommand(ctx context.Context, acc *config.Provider, configDirEnv string, argv ...string) (string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = buildAccountEnv(acc, configDirEnv)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

func parseClaudeAuthStatus(out string, runErr error) accountAuthStatus {
	status := accountAuthStatus{Checked: true}
	var parsed struct {
		LoggedIn         bool   `json:"loggedIn"`
		AuthMethod       string `json:"authMethod"`
		SubscriptionType string `json:"subscriptionType"`
		Email            string `json:"email"`
	}
	// The JSON goes to stdout even when the exit code reports "logged out",
	// so parse first and only fall back to the raw output when there is none.
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		status.Error = firstLine(out)
		if status.Error == "" && runErr != nil {
			status.Error = runErr.Error()
		}
		return status
	}
	status.LoggedIn = parsed.LoggedIn
	status.Method = parsed.AuthMethod
	var detail []string
	for _, part := range []string{parsed.SubscriptionType, parsed.Email} {
		if part != "" {
			detail = append(detail, part)
		}
	}
	status.Detail = strings.Join(detail, " · ")
	return status
}

func parseCodexLoginStatus(out string, runErr error) accountAuthStatus {
	status := accountAuthStatus{Checked: true, Detail: firstLine(out)}
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		status.LoggedIn = true
		if method, ok := strings.CutPrefix(status.Detail, "Logged in using "); ok {
			status.Method = method
		}
	case errors.As(runErr, &exitErr):
		// `codex login status` exits non-zero when there are no credentials.
	default:
		status.Error = runErr.Error()
	}
	return status
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}
