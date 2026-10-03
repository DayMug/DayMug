package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/service/filewatch"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// watchFixture is one running watch endpoint plus the work dir behind it.
type watchFixture struct {
	srv     *httptest.Server
	workDir string
	watcher *filewatch.Watcher
}

func setupWatchServer(t *testing.T, authedUID string) *watchFixture {
	t.Helper()
	workDir := t.TempDir()
	ms := &storetest.Fake{Users: []store.User{{ID: "u1", Username: "owner", WorkDir: workDir}}}

	w, err := filewatch.New()
	if err != nil {
		t.Fatalf("filewatch.New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	w.SetCoalesceWindow(10 * time.Millisecond)

	h := NewFileHandler(ms)
	h.Watcher = w

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", authedUID)
		c.Next()
	})
	r.GET("/api/users/:id/files/watch", h.WatchFiles)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &watchFixture{srv: srv, workDir: workDir, watcher: w}
}

func (f *watchFixture) dial(t *testing.T, ctx context.Context) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws"+f.srv.URL[len("http"):]+"/api/users/u1/files/watch", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

func sendWatch(t *testing.T, ctx context.Context, conn *websocket.Conn, paths ...string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"action": "watch", "paths": paths})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, body); err != nil {
		t.Fatalf("write watch: %v", err)
	}
}

// readFrame decodes the next text frame into a generic map.
func readFrame(t *testing.T, ctx context.Context, conn *websocket.Conn) map[string]any {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return out
}

// readFrameOfType skips keep-alive noise until a frame with the wanted type
// arrives, so an assertion never fails on frame ordering it doesn't care about.
func readFrameOfType(t *testing.T, ctx context.Context, conn *websocket.Conn, want string) map[string]any {
	t.Helper()
	for i := 0; i < 10; i++ {
		frame := readFrame(t, ctx, conn)
		if frame["type"] == want {
			return frame
		}
	}
	t.Fatalf("no %q frame in 10 reads", want)
	return nil
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestWatchWSPushesChangeAfterWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := setupWatchServer(t, "u1")
	ctx := testCtx(t)

	conn := f.dial(t, ctx)
	defer func() { _ = conn.CloseNow() }()

	target := filepath.Join(f.workDir, "a.md")
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	sendWatch(t, ctx, conn, "a.md")

	ready := readFrameOfType(t, ctx, conn, "ready")
	watching, _ := ready["watching"].([]any)
	if len(watching) != 1 || watching[0] != "a.md" {
		t.Fatalf("ready.watching = %v, want [a.md]", ready["watching"])
	}

	if err := os.WriteFile(target, []byte("longer v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	changed := readFrameOfType(t, ctx, conn, "changed")
	if changed["path"] != "a.md" {
		t.Fatalf("changed.path = %v, want a.md", changed["path"])
	}
	if size, _ := changed["size"].(float64); int64(size) != int64(len("longer v2")) {
		t.Fatalf("changed.size = %v, want %d", changed["size"], len("longer v2"))
	}
	if changed["mtime"] == "" || changed["mtime"] == nil {
		t.Fatal("changed.mtime is empty")
	}
}

func TestWatchWSPushesChangeAfterAtomicRename(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := setupWatchServer(t, "u1")
	ctx := testCtx(t)

	conn := f.dial(t, ctx)
	defer func() { _ = conn.CloseNow() }()

	target := filepath.Join(f.workDir, "a.md")
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	sendWatch(t, ctx, conn, "a.md")
	readFrameOfType(t, ctx, conn, "ready")

	tmp := filepath.Join(f.workDir, "a.md.tmp")
	if err := os.WriteFile(tmp, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, target); err != nil {
		t.Fatal(err)
	}

	if got := readFrameOfType(t, ctx, conn, "changed"); got["path"] != "a.md" {
		t.Fatalf("changed.path = %v, want a.md", got["path"])
	}
}

func TestWatchWSReportsRemoval(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := setupWatchServer(t, "u1")
	ctx := testCtx(t)

	conn := f.dial(t, ctx)
	defer func() { _ = conn.CloseNow() }()

	target := filepath.Join(f.workDir, "a.md")
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	sendWatch(t, ctx, conn, "a.md")
	readFrameOfType(t, ctx, conn, "ready")

	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}

	if got := readFrameOfType(t, ctx, conn, "removed"); got["path"] != "a.md" {
		t.Fatalf("removed.path = %v, want a.md", got["path"])
	}
}

func TestWatchWSRejectsPathOutsideWorkDir(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := setupWatchServer(t, "u1")
	ctx := testCtx(t)

	conn := f.dial(t, ctx)
	defer func() { _ = conn.CloseNow() }()

	sendWatch(t, ctx, conn, "../../etc/passwd")

	got := readFrameOfType(t, ctx, conn, "error")
	if got["code"] != "forbidden" {
		t.Fatalf("error.code = %v, want forbidden", got["code"])
	}
	// A rejected batch must not leave a watch behind — partial acceptance
	// would make the client's degrade decision meaningless.
	if n := f.watcher.WatchedDirCount(); n != 0 {
		t.Fatalf("watched dirs = %d, want 0", n)
	}
}

func TestWatchWSRejectsIgnoredDirectory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := setupWatchServer(t, "u1")
	ctx := testCtx(t)

	conn := f.dial(t, ctx)
	defer func() { _ = conn.CloseNow() }()

	sendWatch(t, ctx, conn, "node_modules/pkg/index.js")

	if got := readFrameOfType(t, ctx, conn, "error"); got["code"] != "ignored" {
		t.Fatalf("error.code = %v, want ignored", got["code"])
	}
}

func TestWatchWSRejectsOverPathLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := setupWatchServer(t, "u1")
	ctx := testCtx(t)

	conn := f.dial(t, ctx)
	defer func() { _ = conn.CloseNow() }()

	paths := make([]string, 0, filewatch.MaxPathsPerSubscription+1)
	for i := 0; i <= filewatch.MaxPathsPerSubscription; i++ {
		paths = append(paths, string(rune('a'+i))+".md")
	}
	sendWatch(t, ctx, conn, paths...)

	if got := readFrameOfType(t, ctx, conn, "error"); got["code"] != "capacity" {
		t.Fatalf("error.code = %v, want capacity", got["code"])
	}
}

// A directory watch leaked per dropped connection is how a long-running
// server eventually exhausts its inotify budget.
func TestWatchWSReleasesWatchOnDisconnect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := setupWatchServer(t, "u1")
	ctx := testCtx(t)

	conn := f.dial(t, ctx)
	target := filepath.Join(f.workDir, "a.md")
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	sendWatch(t, ctx, conn, "a.md")
	readFrameOfType(t, ctx, conn, "ready")
	if n := f.watcher.WatchedDirCount(); n != 1 {
		t.Fatalf("watched dirs = %d, want 1", n)
	}

	_ = conn.CloseNow()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f.watcher.WatchedDirCount() == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("watched dirs = %d after disconnect, want 0", f.watcher.WatchedDirCount())
}

func TestWatchWSRequiresAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// Authenticated as a different owner: getUserFileAccess must reject before
	// the connection is ever upgraded.
	f := setupWatchServer(t, "intruder")
	ctx := testCtx(t)

	_, _, err := websocket.Dial(ctx, "ws"+f.srv.URL[len("http"):]+"/api/users/u1/files/watch", nil)
	if err == nil {
		t.Fatal("expected the handshake to fail for an unauthorized user")
	}
}

// With no watcher wired up (fsnotify unavailable at boot) the endpoint must
// still answer, so the client degrades to polling instead of hanging.
func TestWatchWSReportsUnavailableWithoutWatcher(t *testing.T) {
	gin.SetMode(gin.TestMode)
	workDir := t.TempDir()
	ms := &storetest.Fake{Users: []store.User{{ID: "u1", Username: "owner", WorkDir: workDir}}}
	h := NewFileHandler(ms)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("auth_user_id", "u1")
		c.Next()
	})
	r.GET("/api/users/:id/files/watch", h.WatchFiles)
	srv := httptest.NewServer(r)
	defer srv.Close()

	ctx := testCtx(t)
	conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+"/api/users/u1/files/watch", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()

	if got := readFrameOfType(t, ctx, conn, "error"); got["code"] != "unavailable" {
		t.Fatalf("error.code = %v, want unavailable", got["code"])
	}
}
