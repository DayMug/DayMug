package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
)

// AdminTerminalHandler drives the admin browser-terminal ("tty mode"). Each
// WebSocket owns exactly one interactive CLI process on a PTY: the client sends
// `start` with an account name and an optional working directory, we spawn that
// account's CLI as a bare interactive TUI under the account's env, and proxy
// the PTY <-> WebSocket byte stream both ways.
//
// This is a *pure* terminal: unlike the chat (-p) path it persists nothing — no
// messages, no token accounting, no auto-titles. Unlike the admin login flow it
// is not reusable across reconnects and does not buffer output for replay:
// closing the socket tears the process down. Admin-only — the route group's
// RequireAdmin gate is the only authorization needed here.
type AdminTerminalHandler struct {
	Cfg *config.Config
	// Pool caps how many of these interactive processes may be alive per
	// account (service.Pool.EnterLive, LiveMultiplier×MaxConcurrent). Without
	// it every socket spawned an unbounded full-size agent process: nothing on
	// this path goes through the chat semaphore, so the tty page was a way to
	// fan out past every account limit. nil disables the cap — only tests that
	// don't care about it should leave it unset.
	Pool *service.Pool
	// PingInterval controls the WebSocket keep-alive cadence. Zero falls back
	// to defaultPingInterval; tests override it.
	PingInterval time.Duration
}

// NewAdminTerminalHandler wires a handler bound to the loaded config so the
// per-account ConfigDir / env is reachable, plus the account pool that caps
// live tty processes. Tests can pass nil for either and set the fields.
func NewAdminTerminalHandler(cfg *config.Config, pool *service.Pool) *AdminTerminalHandler {
	return &AdminTerminalHandler{Cfg: cfg, Pool: pool}
}

// adminTerminalInputMaxBytes caps a single `input` frame. Generous because
// this is a real terminal (paste of a long command / file is legitimate), but
// bounded so a buggy client can't flood the PTY with one frame.
const adminTerminalInputMaxBytes = 256 * 1024

const (
	adminTerminalDefaultRows uint16 = 24
	adminTerminalDefaultCols uint16 = 80
)

// adminTerminalWriteTimeout bounds one frame write. The writer goroutine runs
// on a context that outlives the session ctx (see handleWS), so this deadline
// is what stops a wedged socket from holding teardown open forever.
const adminTerminalWriteTimeout = 10 * time.Second

// adminTerminalSpawner is the ProcessSpawner used for the admin terminal.
// PTY-backed so the CLI runs its full interactive TUI. A var so tests can swap
// in a fake that emits scripted output.
var adminTerminalSpawner agent.ProcessSpawner = agent.PtySpawner{}

type adminTerminalClientMsg struct {
	Type    string `json:"type"`
	Account string `json:"account,omitempty"`
	// WorkDir is the child's working directory. Empty inherits the server
	// process's cwd. Must be an existing directory when set.
	WorkDir string `json:"work_dir,omitempty"`
	// Login spawns the account's login flow instead of the bare interactive
	// TUI, so the admin can authenticate it from the models page or the
	// terminal page. Ignored for the compatible types, which authenticate with
	// an API key from the account's env.
	Login bool   `json:"login,omitempty"`
	Data  string `json:"data,omitempty"`
	Rows  uint16 `json:"rows,omitempty"`
	Cols  uint16 `json:"cols,omitempty"`
}

type adminTerminalServerMsg struct {
	Type     string `json:"type"`
	Data     string `json:"data,omitempty"`
	Account  string `json:"account,omitempty"`
	Message  string `json:"message,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

// terminalArgvForType maps an account CLI type to the bare interactive command.
// The Claude family runs Claude Code; the Codex family runs codex.
//
// login swaps in the first-party login flow. Both are the headless variants:
// DayMug runs on a server the admin's browser can't reach on localhost, so
// Claude prints an OAuth URL and asks for the code back, and Codex uses the
// device-code flow instead of its localhost callback.
func terminalArgvForType(cliType string, login bool) []string {
	switch {
	case login && cliType == config.CLITypeCodex:
		return []string{agent.ResolveAgentBinary("codex"), "login", "--device-auth"}
	case login && cliType == config.CLITypeClaude:
		return []string{agent.ResolveAgentBinary("claude"), "auth", "login"}
	case config.IsCodexFamily(cliType):
		return []string{agent.ResolveAgentBinary("codex")}
	}
	return []string{agent.ResolveAgentBinary("claude")}
}

// terminalConfigDirEnv maps an account CLI type to the env var its ConfigDir is
// exported as: CODEX_HOME for the Codex family, CLAUDE_CONFIG_DIR otherwise.
func terminalConfigDirEnv(cliType string) string {
	if config.IsCodexFamily(cliType) {
		return "CODEX_HOME"
	}
	return "CLAUDE_CONFIG_DIR"
}

// HandleWS is the gin entry point for the admin terminal WebSocket. Auth +
// admin gate live on the route group; by the time we get here the caller is a
// confirmed admin.
func (h *AdminTerminalHandler) HandleWS(c *gin.Context) {
	if middleware.CurrentUserID(c) == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no session"})
		return
	}
	if h.Cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config not loaded"})
		return
	}
	h.handleWS(c.Writer, c.Request)
}

func (h *AdminTerminalHandler) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, wsAcceptOptions(r))
	if err != nil {
		log.Printf("[admin-terminal] ws accept: %v", err)
		return
	}
	defer func() { _ = conn.CloseNow() }()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	sendCh := make(chan []byte, 64)
	var writerDone sync.WaitGroup
	writerDone.Add(1)
	// The writer deliberately does not write on ctx. Teardown below cancels ctx
	// (to kill the child and unblock the reader) *before* it drains sendCh, so a
	// writer bound to ctx would drop exactly the frames that explain why we are
	// closing — e.g. "live session cap reached" — and the browser would see a
	// silent disconnect. Per-write deadlines keep this from being unbounded.
	writeCtx := context.WithoutCancel(ctx)
	go func() {
		defer writerDone.Done()
		for data := range sendCh {
			wctx, wcancel := context.WithTimeout(writeCtx, adminTerminalWriteTimeout)
			werr := conn.Write(wctx, websocket.MessageText, data)
			wcancel()
			if werr != nil {
				return
			}
		}
	}()

	// Ping keep-alive so an idle terminal survives NAT timeouts.
	go func() {
		interval := h.PingInterval
		if interval <= 0 {
			interval = defaultPingInterval
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
				perr := conn.Ping(pingCtx)
				pingCancel()
				if perr != nil {
					cancel()
					return
				}
			}
		}
	}()

	var proc agent.RunningProcess
	var readerDone sync.WaitGroup
	defer func() {
		if proc != nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
		cancel()
		readerDone.Wait()
		close(sendCh)
		writerDone.Wait()
	}()

	sendJSON := func(msg adminTerminalServerMsg) {
		data, merr := json.Marshal(msg)
		if merr != nil {
			return
		}
		select {
		case sendCh <- data:
		default:
		}
	}

	for {
		_, raw, rerr := conn.Read(ctx)
		if rerr != nil {
			return
		}
		var msg adminTerminalClientMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			sendJSON(adminTerminalServerMsg{Type: "error", Message: "invalid message: " + err.Error()})
			continue
		}
		switch msg.Type {
		case "start":
			if proc != nil {
				sendJSON(adminTerminalServerMsg{Type: "error", Message: "already started"})
				continue
			}
			p, releaseLive, serr := h.spawn(ctx, msg)
			if serr != nil {
				sendJSON(adminTerminalServerMsg{Type: "error", Message: serr.Error()})
				if errors.Is(serr, service.ErrTooManyLiveSessions) {
					// Terminal for this socket, not a retryable input error:
					// the cap is per account and won't clear while this
					// connection idles, so say why and hang up rather than
					// leaving a terminal page that looks alive but has no
					// process behind it. The deferred teardown drains sendCh,
					// so the error frame above still reaches the client.
					return
				}
				continue
			}
			proc = p
			sendJSON(adminTerminalServerMsg{Type: "started", Account: msg.Account})
			readerDone.Add(1)
			go h.pump(proc, releaseLive, sendJSON, &readerDone)
		case "input":
			if proc == nil {
				sendJSON(adminTerminalServerMsg{Type: "error", Message: "not started"})
				continue
			}
			if len(msg.Data) > adminTerminalInputMaxBytes {
				sendJSON(adminTerminalServerMsg{Type: "error", Message: "input too large"})
				continue
			}
			if stdin := proc.Stdin(); stdin != nil {
				if _, werr := stdin.Write([]byte(msg.Data)); werr != nil {
					sendJSON(adminTerminalServerMsg{Type: "error", Message: werr.Error()})
				}
			}
		case "resize":
			if proc == nil {
				continue
			}
			if rz, ok := proc.(agent.Resizable); ok && msg.Rows > 0 && msg.Cols > 0 {
				_ = rz.Setsize(msg.Rows, msg.Cols)
			}
		case "cancel":
			if proc != nil {
				_ = proc.Signal(syscall.SIGTERM)
			}
		default:
			sendJSON(adminTerminalServerMsg{Type: "error", Message: fmt.Sprintf("unknown type %q", msg.Type)})
		}
	}
}

// spawn resolves the account, validates the working directory, reserves a live
// process slot on the account, and starts the bare interactive CLI on a PTY
// under the account's env.
//
// The second return value frees that slot and must be called once the child is
// actually gone; it is never nil on success (a no-op when no Pool is wired).
// ErrTooManyLiveSessions means the account is already at its live cap.
func (h *AdminTerminalHandler) spawn(ctx context.Context, msg adminTerminalClientMsg) (agent.RunningProcess, func(), error) {
	acc := h.Cfg.FindAccount(msg.Account)
	if acc == nil {
		return nil, nil, ErrTerminalAccountNotFound
	}
	if msg.WorkDir != "" {
		info, err := os.Stat(msg.WorkDir)
		if err != nil {
			return nil, nil, fmt.Errorf("work dir %q: %w", msg.WorkDir, err)
		}
		if !info.IsDir() {
			return nil, nil, fmt.Errorf("work dir %q is not a directory", msg.WorkDir)
		}
	}
	if err := ensureAccountConfigDir(acc.ConfigDir); err != nil {
		return nil, nil, err
	}
	// Take the live slot after the cheap validations so a bad account name or
	// work dir doesn't churn the counter, but before Spawn so the process can
	// never exist without being counted.
	releaseLive := func() {}
	if h.Pool != nil {
		rel, err := h.Pool.EnterLive(acc.Name)
		if err != nil {
			return nil, nil, err
		}
		releaseLive = rel
	}
	rows, cols := msg.Rows, msg.Cols
	if rows == 0 {
		rows = adminTerminalDefaultRows
	}
	if cols == 0 {
		cols = adminTerminalDefaultCols
	}
	proc, err := adminTerminalSpawner.Spawn(ctx, agent.SpawnRequest{
		Argv:        terminalArgvForType(acc.Type, msg.Login),
		Env:         buildAccountEnv(acc, terminalConfigDirEnv(acc.Type)),
		WorkDir:     msg.WorkDir,
		Interactive: true,
		InitialRows: rows,
		InitialCols: cols,
	})
	if err != nil {
		releaseLive()
		return nil, nil, err
	}
	return proc, releaseLive, nil
}

// pump ferries PTY output to the client, then waits for the child to exit and
// reports its status. Runs on its own goroutine per spawned process.
//
// It also owns the account's live slot: releaseLive fires after proc.Wait()
// returns, i.e. when the process is genuinely gone. Releasing earlier (when the
// socket closes) would let a reconnecting admin double-count against the cap
// while the previous child is still winding down from its SIGTERM.
func (h *AdminTerminalHandler) pump(proc agent.RunningProcess, releaseLive func(), sendJSON func(adminTerminalServerMsg), done *sync.WaitGroup) {
	defer done.Done()
	if releaseLive != nil {
		defer releaseLive()
	}
	stdout := proc.Stdout()
	buf := make([]byte, 4096)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			sendJSON(adminTerminalServerMsg{Type: "output", Data: string(buf[:n])})
		}
		if err != nil {
			break
		}
	}
	exitErr := proc.Wait()
	exitCode := 0
	errStr := ""
	if exitErr != nil {
		var ee *exec.ExitError
		if errors.As(exitErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
		errStr = exitErr.Error()
	}
	sendJSON(adminTerminalServerMsg{Type: "exited", ExitCode: exitCode, Message: errStr})
}
