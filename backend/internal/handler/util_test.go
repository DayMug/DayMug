package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// bindJSONProbe mounts a handler that does nothing but bindJSON, so the body
// cap is exercised without any endpoint's own validation getting in the way.
func bindJSONProbe() *gin.Engine {
	r := gin.New()
	r.POST("/probe", func(c *gin.Context) {
		var body struct {
			Content string `json:"content"`
		}
		if !bindJSON(c, &body) {
			return
		}
		c.JSON(http.StatusOK, gin.H{"len": len(body.Content)})
	})
	return r
}

func postProbe(t *testing.T, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/probe", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	bindJSONProbe().ServeHTTP(rec, req)
	return rec
}

// jsonBodyOfSize builds a syntactically valid JSON object of exactly n bytes
// by padding the single string field. Lets the boundary tests target the cap
// to the byte instead of approximating it.
func jsonBodyOfSize(t *testing.T, n int) []byte {
	t.Helper()
	const envelope = `{"content":""}`
	if n < len(envelope) {
		t.Fatalf("n=%d smaller than the JSON envelope", n)
	}
	body := []byte(`{"content":"` + strings.Repeat("a", n-len(envelope)) + `"}`)
	if len(body) != n {
		t.Fatalf("built %d bytes, want %d", len(body), n)
	}
	return body
}

func TestBindJSON_RejectsOversizeBodyWith413(t *testing.T) {
	rec := postProbe(t, jsonBodyOfSize(t, maxJSONBodyBytes+1))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	// A 400 here would tell the client to fix its JSON, which it can't.
	if !strings.Contains(rec.Body.String(), "request body too large") {
		t.Errorf("unexpected error body: %s", rec.Body.String())
	}
}

func TestBindJSON_AcceptsBodyExactlyAtLimit(t *testing.T) {
	rec := postProbe(t, jsonBodyOfSize(t, maxJSONBodyBytes))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 at the limit, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBindJSON_MalformedBodyStill400(t *testing.T) {
	rec := postProbe(t, []byte(`{"content":`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed JSON, got %d: %s", rec.Code, rec.Body.String())
	}
}

// The generic cap must sit far enough above maxEditableFileBytes that an
// oversize editor save still reaches WriteFile's own check and gets its more
// specific message. If someone lowers maxJSONBodyBytes to ~10 MiB this fails
// with the generic error instead.
func TestWriteFile_OversizeContentKeepsSpecific413(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	r := setupFileRouter(ms)

	huge := strings.Repeat("a", maxEditableFileBytes+1)
	payload, err := json.Marshal(map[string]string{"path": "big.txt", "content": huge})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest("PUT", "/api/users/u1/files/write", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "file too large") {
		t.Errorf("expected WriteFile's specific message, got: %s", rec.Body.String())
	}
}

// A save right at the editor limit is the largest legitimate bindJSON body in
// the codebase; it must still succeed end to end.
func TestWriteFile_AtEditorLimitStillSucceeds(t *testing.T) {
	dir := t.TempDir()
	ms := storetest.New()
	ms.Users = []store.User{{ID: "u1", Name: "Test", WorkDir: dir}}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r := setupFileRouter(ms)

	content := strings.Repeat("a", maxEditableFileBytes)
	payload, err := json.Marshal(map[string]string{"path": "big.txt", "content": content})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest("PUT", "/api/users/u1/files/write", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	info, err := os.Stat(filepath.Join(dir, "big.txt"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != int64(len(content)) {
		t.Errorf("wrote %d bytes, want %d", info.Size(), len(content))
	}
}

// Guards the relationship the comment on maxJSONBodyBytes relies on: JSON
// escaping can double a text payload, so the wire cap has to leave room for a
// fully escaped maximum-size editor save.
func TestMaxJSONBodyBytes_LeavesRoomForEscapedEditorSave(t *testing.T) {
	if maxJSONBodyBytes < 2*maxEditableFileBytes {
		t.Fatalf("maxJSONBodyBytes=%d leaves no room for a fully escaped %d-byte save",
			maxJSONBodyBytes, maxEditableFileBytes)
	}
	worstCase := len(fmt.Sprintf(`{"path":"x","content":"%s"}`, strings.Repeat(`\n`, maxEditableFileBytes)))
	if worstCase > maxJSONBodyBytes {
		t.Fatalf("escape-doubled save is %d bytes, over the %d cap", worstCase, maxJSONBodyBytes)
	}
}
