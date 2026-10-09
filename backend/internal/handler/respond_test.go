package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// render runs one responder against a throwaway gin context and returns the
// status and decoded "error" field.
func render(t *testing.T, respond func(c *gin.Context)) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
	respond(c)
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return w.Code, body.Error
}

func TestRespondInternalErrorHidesCause(t *testing.T) {
	cause := errors.New("open /home/alice/.daymug/db.sqlite: permission denied")
	code, msg := render(t, func(c *gin.Context) { respondInternalError(c, "test", cause) })
	if code != http.StatusInternalServerError || msg != internalErrorMessage {
		t.Fatalf("got %d %q, want 500 %q", code, msg, internalErrorMessage)
	}
}

func TestRespondStoreError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{"not found", store.ErrNotFound, http.StatusNotFound, "user not found"},
		{"wrapped not found", errors.Join(errors.New("ctx"), store.ErrNotFound), http.StatusNotFound, "user not found"},
		{"other", errors.New("database is locked"), http.StatusInternalServerError, internalErrorMessage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, msg := render(t, func(c *gin.Context) { respondStoreError(c, tt.err, "user not found") })
			if code != tt.wantCode || msg != tt.wantMsg {
				t.Fatalf("got %d %q, want %d %q", code, msg, tt.wantCode, tt.wantMsg)
			}
		})
	}
}

func TestRespondServiceErrorInternalMessages(t *testing.T) {
	cause := errors.New("sqlite: disk I/O error")
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{"hand-written 500 kept", service.Internal("save summary failed", cause), http.StatusInternalServerError, "save summary failed"},
		{"cause as message hidden", service.Internal(cause.Error(), cause), http.StatusInternalServerError, internalErrorMessage},
		{"cause embedded in message hidden", service.Internal("reload scheduled tasks: "+cause.Error(), cause), http.StatusInternalServerError, internalErrorMessage},
		{"empty message hidden", service.NewServiceError(http.StatusInternalServerError, "", cause), http.StatusInternalServerError, internalErrorMessage},
		{"4xx untouched", service.BadRequest("name is required"), http.StatusBadRequest, "name is required"},
		{"store miss", service.StoreError(store.ErrNotFound, "conversation not found"), http.StatusNotFound, "conversation not found"},
		{"store failure hidden", service.StoreError(cause, "conversation not found"), http.StatusInternalServerError, internalErrorMessage},
		{"bare error hidden", cause, http.StatusInternalServerError, internalErrorMessage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, msg := render(t, func(c *gin.Context) { respondServiceError(c, tt.err) })
			if code != tt.wantCode || msg != tt.wantMsg {
				t.Fatalf("got %d %q, want %d %q", code, msg, tt.wantCode, tt.wantMsg)
			}
		})
	}
}

// A session whose user row has since been deleted used to surface as a 500
// carrying "sql: no rows in result set".
func TestMe_MissingUserIs404(t *testing.T) {
	h := NewAuthHandler(storetest.New(), nil)
	r := setupNotificationsRouter(t, h, "ghost")
	w := doJSON(r, "GET", "/api/auth/me", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d body %s, want 404", w.Code, w.Body.String())
	}
}
