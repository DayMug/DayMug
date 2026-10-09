package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func TestUploadResponseIncludesAuthenticatedPreviewURL(t *testing.T) {
	convDir := t.TempDir()
	ms := storetest.New()
	human := store.User{ID: "agent 1", Username: "alice", Email: "alice@example.com", WorkDir: convDir}
	_ = ms.CreateUser(context.Background(), human)
	_ = ms.CreateConversation(context.Background(), "conv-preview", "t", human.ID, convDir, "", "")
	router := setupUploadRouter(t, ms, human.ID, 0)
	body, contentType := multipartBody(
		t,
		"file",
		"shot.png",
		append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 520)...),
		map[string]string{"conversation_id": "conv-preview"},
	)
	req := httptest.NewRequest(http.MethodPost, "/api/uploads", body)
	req.Header.Set("Content-Type", contentType)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var payload struct {
		Ref string `json:"ref"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Ref == "" || !strings.Contains(payload.URL, "/api/users/agent%201/files/read?path=") {
		t.Fatalf("unexpected preview payload: %+v", payload)
	}
}
