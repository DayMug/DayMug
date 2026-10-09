package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/service"
)

// setupPauseRouter mounts the toggle with no dispatcher: the resume sweep is
// exercised in the service package, and leaving it nil here also asserts the
// handler stays usable without one.
func setupPauseRouter(gate *service.PauseGate) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewAdminPauseHandler(gate, nil)
	r.GET("/api/admin/pause", h.Get)
	r.PUT("/api/admin/pause", h.Put)
	return r
}

func pauseState(t *testing.T, body []byte) bool {
	t.Helper()
	var got struct {
		Paused bool `json:"paused"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return got.Paused
}

func TestAdminPauseDefaultsToRunning(t *testing.T) {
	r := setupPauseRouter(service.NewPauseGate())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/pause", http.NoBody))
	if w.Code != http.StatusOK {
		t.Fatalf("GET status %d", w.Code)
	}
	if pauseState(t, w.Body.Bytes()) {
		t.Error("a freshly started server must report paused=false")
	}
}

func TestAdminPausePutRoundTrips(t *testing.T) {
	gate := service.NewPauseGate()
	r := setupPauseRouter(gate)

	for _, want := range []bool{true, false} {
		w := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]bool{"paused": want})
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/admin/pause", bytes.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("PUT paused=%v status %d: %s", want, w.Code, w.Body.String())
		}
		if got := pauseState(t, w.Body.Bytes()); got != want {
			t.Errorf("PUT paused=%v echoed %v", want, got)
		}
		if got := gate.Paused(); got != want {
			t.Errorf("gate is %v after PUT paused=%v", got, want)
		}
	}
}

// A missing field must not read as "resume": an accidental unpause during an
// upgrade window is the expensive direction to fail in.
func TestAdminPauseRejectsMissingField(t *testing.T) {
	gate := service.NewPauseGate()
	gate.SetPaused(true)
	r := setupPauseRouter(gate)

	for _, body := range []string{`{}`, `{"pause":true}`, `not json`} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/admin/pause", bytes.NewBufferString(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s status %d, want 400", body, w.Code)
		}
	}
	if !gate.Paused() {
		t.Error("a rejected request changed the pause state")
	}
}
