package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/store"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// Production wires *store.SQLiteStore; if it ever stopped satisfying
// DBPinger the probe would silently skip the database check.
var _ DBPinger = (*store.SQLiteStore)(nil)

type stubPinger struct{ err error }

func (p stubPinger) Ping(context.Context) error { return p.err }

func serveHealth(t *testing.T, db any) (int, map[string]string) {
	t.Helper()
	r := gin.New()
	r.GET("/api/health", NewHealthCheck(db))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/health", http.NoBody))
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return w.Code, body
}

func TestHealthCheckReportsHealthyDatabase(t *testing.T) {
	code, body := serveHealth(t, stubPinger{})
	if code != http.StatusOK || body["status"] != "ok" || body["database"] != "ok" {
		t.Fatalf("got %d %v", code, body)
	}
}

// The upgrade watchdog rolls back on anything but 200, so a broken database
// must fail the probe instead of passing on a live HTTP listener alone.
func TestHealthCheckFailsWhenDatabaseUnreadable(t *testing.T) {
	code, body := serveHealth(t, stubPinger{err: errors.New("disk I/O error")})
	if code != http.StatusServiceUnavailable || body["status"] != "unavailable" {
		t.Fatalf("got %d %v", code, body)
	}
	if _, leaked := body["error"]; leaked {
		t.Fatalf("health must not echo internal errors: %v", body)
	}
}

func TestHealthCheckWithoutPingerStaysHealthy(t *testing.T) {
	code, _ := serveHealth(t, struct{}{})
	if code != http.StatusOK {
		t.Fatalf("got %d, want 200", code)
	}
}

func TestHealthCheckAgainstRealStore(t *testing.T) {
	db, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := serveHealth(t, db); code != http.StatusOK {
		t.Fatalf("open store: got %d, want 200", code)
	}
	_ = db.Close()
	if code, _ := serveHealth(t, db); code != http.StatusServiceUnavailable {
		t.Fatalf("closed store: got %d, want 503", code)
	}
}
