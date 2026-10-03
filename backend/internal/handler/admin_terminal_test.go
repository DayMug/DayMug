package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
)

// terminalArgvForType picks the interactive command per CLI type, and swaps in
// the headless login flow only for the first-party types.
func TestTerminalArgvForType(t *testing.T) {
	tests := []struct {
		name    string
		cliType string
		login   bool
		want    []string
	}{
		{"claude", config.CLITypeClaude, false, []string{"claude"}},
		{"claude login", config.CLITypeClaude, true, []string{"claude", "auth", "login"}},
		{"claude-compatible login flag ignored", config.CLITypeClaudeCompatible, true, []string{"claude"}},
		{"claude-compatible runs claude", config.CLITypeClaudeCompatible, false, []string{"claude"}},
		{"codex bare", config.CLITypeCodex, false, []string{"codex"}},
		{"codex login uses the device-code flow", config.CLITypeCodex, true, []string{"codex", "login", "--device-auth"}},
		{"openai-compatible login flag ignored", config.CLITypeOpenAICompatible, true, []string{"codex"}},
		{"openai-compatible runs codex", config.CLITypeOpenAICompatible, false, []string{"codex"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := terminalArgvForType(tt.cliType, tt.login)
			if len(got) > 0 {
				got[0] = filepath.Base(got[0])
			}
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("terminalArgvForType(%q, %v) = %v, want %v", tt.cliType, tt.login, got, tt.want)
			}
		})
	}
}

// adminTerminalTestSetup wires the handler with a real account Pool and a
// scripted spawner, then serves handleWS directly — the admin gate lives on the
// route group, so the socket needs no session here. maxConcurrent sizes the
// account: the live (tty) cap is service.LiveMultiplier times that.
func adminTerminalTestSetup(t *testing.T, maxConcurrent int) (*AdminTerminalHandler, *fakeSpawner, *httptest.Server) {
	t.Helper()
	return adminTerminalServer(t, &config.Config{Providers: []config.Provider{
		{Name: "default", Type: config.CLITypeClaude, MaxConcurrent: maxConcurrent},
	}})
}

// adminTerminalServer is adminTerminalTestSetup for a caller-supplied config.
func adminTerminalServer(t *testing.T, cfg *config.Config) (*AdminTerminalHandler, *fakeSpawner, *httptest.Server) {
	t.Helper()
	h := NewAdminTerminalHandler(cfg, service.NewPool(cfg))

	spawner := newFakeSpawner()
	prevSpawner := adminTerminalSpawner
	adminTerminalSpawner = spawner
	t.Cleanup(func() { adminTerminalSpawner = prevSpawner })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.handleWS(w, r)
	}))
	t.Cleanup(srv.Close)
	return h, spawner, srv
}

func sendTerminalMsg(t *testing.T, ctx context.Context, conn *websocket.Conn, msg adminTerminalClientMsg) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// readTerminalUntil drains frames until one of the wanted types arrives, so a
// test doesn't have to care about interleaved "output" chunks.
func readTerminalUntil(t *testing.T, ctx context.Context, conn *websocket.Conn, types ...string) adminTerminalServerMsg {
	t.Helper()
	want := map[string]struct{}{}
	for _, ty := range types {
		want[ty] = struct{}{}
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read (waiting for %v): %v", types, err)
		}
		var msg adminTerminalServerMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal %q: %v", string(data), err)
		}
		if _, ok := want[msg.Type]; ok {
			return msg
		}
	}
}

// startTerminalSession dials, starts a session, and returns the live socket
// plus the fake child behind it.
func startTerminalSession(t *testing.T, srv *httptest.Server, spawner *fakeSpawner) (*websocket.Conn, *fakeProcess, context.CancelFunc) {
	t.Helper()
	conn, ctx, cancel := dialTerminal(t, srv)
	sendTerminalMsg(t, ctx, conn, adminTerminalClientMsg{Type: "start", Account: "default"})
	if msg := readTerminalUntil(t, ctx, conn, "started", "error"); msg.Type != "started" {
		cancel()
		t.Fatalf("start refused: %s", msg.Message)
	}
	return conn, spawner.waitForSpawn(t, 2*time.Second), cancel
}

// TestAdminTerminalRefusesBeyondLiveCap is the gate on the tty path: each
// socket spawns a full interactive agent process, and this path never touches
// the chat semaphore, so before EnterLive was wired in an admin could fan out
// unbounded ~1GB children. The refusal must be explicit — a silently dead
// terminal page is indistinguishable from a hung one.
func TestAdminTerminalRefusesBeyondLiveCap(t *testing.T) {
	// max_concurrent 1 → live cap = LiveMultiplier × 1 = 2.
	h, spawner, srv := adminTerminalTestSetup(t, 1)

	conn1, proc1, cancel1 := startTerminalSession(t, srv, spawner)
	defer cancel1()
	defer func() { _ = conn1.CloseNow() }()
	conn2, proc2, cancel2 := startTerminalSession(t, srv, spawner)
	defer cancel2()
	defer func() { _ = conn2.CloseNow() }()

	if got := h.Pool.LiveCount("default"); got != 2 {
		t.Fatalf("LiveCount = %d, want 2", got)
	}

	conn3, ctx3, cancel3 := dialTerminal(t, srv)
	defer cancel3()
	defer func() { _ = conn3.CloseNow() }()
	sendTerminalMsg(t, ctx3, conn3, adminTerminalClientMsg{Type: "start", Account: "default"})

	msg := readTerminalUntil(t, ctx3, conn3, "error", "started")
	if msg.Type != "started" && !strings.Contains(msg.Message, "too many live terminal sessions") {
		t.Fatalf("third session error = %q, want the live-cap refusal", msg.Message)
	}
	if msg.Type == "started" {
		t.Fatal("third session spawned a process past the live cap")
	}
	// The refusal is terminal for that socket: the server hangs up rather than
	// leaving a terminal page with no process behind it.
	if _, _, err := conn3.Read(ctx3); err == nil {
		t.Fatal("connection stayed open after the live-cap refusal")
	}
	if got := h.Pool.LiveCount("default"); got != 2 {
		t.Fatalf("LiveCount = %d after the refusal, want 2 — a refused session charged a slot", got)
	}
	_ = proc1
	_ = proc2
}

// TestAdminTerminalReleasesLiveSlotOnProcessExit pins the release side: the
// slot is freed when the child is actually gone, so a normal exit leaves room
// for the next terminal instead of permanently burning capacity.
func TestAdminTerminalReleasesLiveSlotOnProcessExit(t *testing.T) {
	h, spawner, srv := adminTerminalTestSetup(t, 1)

	conn, proc, cancel := startTerminalSession(t, srv, spawner)
	defer cancel()
	defer func() { _ = conn.CloseNow() }()
	if got := h.Pool.LiveCount("default"); got != 1 {
		t.Fatalf("LiveCount = %d, want 1", got)
	}

	proc.Exit(nil)
	ctx, ctxCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ctxCancel()
	readTerminalUntil(t, ctx, conn, "exited")

	// The release happens on the pump goroutine right after Wait returns, which
	// can trail the "exited" frame by a scheduling hop.
	deadline := time.Now().Add(2 * time.Second)
	for h.Pool.LiveCount("default") != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := h.Pool.LiveCount("default"); got != 0 {
		t.Fatalf("LiveCount = %d after the child exited, want 0 — the live slot leaked", got)
	}
}

// startWith dials, sends msg as the start frame, and returns the spawned child.
func startWith(t *testing.T, srv *httptest.Server, spawner *fakeSpawner, msg adminTerminalClientMsg) (*websocket.Conn, context.Context, *fakeProcess, context.CancelFunc) {
	t.Helper()
	conn, ctx, cancel := dialTerminal(t, srv)
	msg.Type = "start"
	sendTerminalMsg(t, ctx, conn, msg)
	if got := readTerminalUntil(t, ctx, conn, "started", "error"); got.Type != "started" {
		cancel()
		t.Fatalf("start refused: %s", got.Message)
	}
	return conn, ctx, spawner.waitForSpawn(t, 2*time.Second), cancel
}

func envHas(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

// The PTY is sized to the browser's xterm and the child sees a real terminal,
// so the CLI draws its normal TUI instead of a stripped-down one.
func TestAdminTerminalSpawnSizeAndTermEnv(t *testing.T) {
	_, spawner, srv := adminTerminalTestSetup(t, 1)
	conn, _, proc, cancel := startWith(t, srv, spawner, adminTerminalClientMsg{Account: "default", Rows: 40, Cols: 120})
	defer cancel()
	defer func() { _ = conn.CloseNow() }()

	if proc.req.InitialRows != 40 || proc.req.InitialCols != 120 {
		t.Fatalf("PTY size = %dx%d, want 40x120", proc.req.InitialRows, proc.req.InitialCols)
	}
	if !proc.req.Interactive {
		t.Fatal("terminal spawn must be Interactive")
	}
	if !envHas(proc.req.Env, "TERM=xterm-256color") {
		t.Fatalf("env missing TERM=xterm-256color: %v", proc.req.Env)
	}
	for _, e := range proc.req.Env {
		if strings.HasPrefix(e, "NO_COLOR=") {
			t.Fatalf("env should not set NO_COLOR; got %v", proc.req.Env)
		}
	}
}

func TestAdminTerminalSpawnSizeDefaultsWhenUnset(t *testing.T) {
	_, spawner, srv := adminTerminalTestSetup(t, 1)
	conn, _, proc, cancel := startWith(t, srv, spawner, adminTerminalClientMsg{Account: "default"})
	defer cancel()
	defer func() { _ = conn.CloseNow() }()

	if proc.req.InitialRows != adminTerminalDefaultRows || proc.req.InitialCols != adminTerminalDefaultCols {
		t.Fatalf("PTY size = %dx%d, want default %dx%d",
			proc.req.InitialRows, proc.req.InitialCols, adminTerminalDefaultRows, adminTerminalDefaultCols)
	}
}

// Each CLI family gets its account's ConfigDir under the env var it reads, so
// a login writes credentials into that account and not the server user's.
func TestAdminTerminalExportsConfigDirPerFamily(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "cla", Type: config.CLITypeClaude, MaxConcurrent: 1, ConfigDir: t.TempDir()},
		{Name: "cdx", Type: config.CLITypeCodex, MaxConcurrent: 1, ConfigDir: t.TempDir()},
	}}
	_, spawner, srv := adminTerminalServer(t, cfg)

	for _, tt := range []struct{ account, want string }{
		{"cla", "CLAUDE_CONFIG_DIR=" + cfg.Providers[0].ConfigDir},
		{"cdx", "CODEX_HOME=" + cfg.Providers[1].ConfigDir},
	} {
		conn, _, proc, cancel := startWith(t, srv, spawner, adminTerminalClientMsg{Account: tt.account, Login: true})
		if !envHas(proc.req.Env, tt.want) {
			t.Errorf("%s: env missing %s: %v", tt.account, tt.want, proc.req.Env)
		}
		_ = conn.CloseNow()
		cancel()
	}
}

// A first login for a fresh account points config_dir at a path that doesn't
// exist yet; starting must create it 0700 so the CLI can persist credentials.
func TestAdminTerminalCreatesMissingConfigDir(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "nested", "claude-cfg")
	_, spawner, srv := adminTerminalServer(t, &config.Config{Providers: []config.Provider{
		{Name: "fresh", Type: config.CLITypeClaude, MaxConcurrent: 1, ConfigDir: configDir},
	}})

	conn, _, _, cancel := startWith(t, srv, spawner, adminTerminalClientMsg{Account: "fresh", Login: true})
	defer cancel()
	defer func() { _ = conn.CloseNow() }()

	info, err := os.Stat(configDir)
	if err != nil {
		t.Fatalf("config dir was not created: %v", err)
	}
	if perm := info.Mode().Perm(); !info.IsDir() || perm != 0o700 {
		t.Fatalf("config dir mode = %v, want a 0700 directory", info.Mode())
	}
}

func TestAdminTerminalUnknownAccount(t *testing.T) {
	_, spawner, srv := adminTerminalTestSetup(t, 1)
	conn, ctx, cancel := dialTerminal(t, srv)
	defer cancel()
	defer func() { _ = conn.CloseNow() }()

	sendTerminalMsg(t, ctx, conn, adminTerminalClientMsg{Type: "start", Account: "nope"})
	if msg := readTerminalUntil(t, ctx, conn, "error", "started"); msg.Message != ErrTerminalAccountNotFound.Error() {
		t.Fatalf("got %+v, want %q", msg, ErrTerminalAccountNotFound.Error())
	}
	select {
	case <-spawner.spawned:
		t.Fatal("an unknown account must not spawn a child")
	case <-time.After(50 * time.Millisecond):
	}
}

// Input reaches the child's stdin and cancel signals it — the two frames the
// login flow depends on (pasting the OAuth code, abandoning the attempt).
func TestAdminTerminalForwardsInputAndCancel(t *testing.T) {
	_, spawner, srv := adminTerminalTestSetup(t, 1)
	conn, ctx, proc, cancel := startWith(t, srv, spawner, adminTerminalClientMsg{Account: "default"})
	defer cancel()
	defer func() { _ = conn.CloseNow() }()

	sendTerminalMsg(t, ctx, conn, adminTerminalClientMsg{Type: "input", Data: "the-code\n"})
	deadline := time.Now().Add(time.Second)
	for string(proc.StdinSnapshot()) != "the-code\n" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := string(proc.StdinSnapshot()); got != "the-code\n" {
		t.Fatalf("stdin = %q, want %q", got, "the-code\n")
	}

	sendTerminalMsg(t, ctx, conn, adminTerminalClientMsg{Type: "cancel"})
	readTerminalUntil(t, ctx, conn, "exited")
	if len(proc.SignalsReceived()) == 0 {
		t.Fatal("no signal received after cancel")
	}
}
