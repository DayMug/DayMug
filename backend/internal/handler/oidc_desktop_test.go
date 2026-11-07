package handler

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func TestDesktopLoginRequiresApprovalAndSingleUseClaim(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ms := storetest.New()
	ms.Users = append(ms.Users, store.User{ID: "u1"})
	h := NewOIDCHandler(ms, &config.Config{}, nil)
	secret := strings.Repeat("a", 64)
	h.desktop = map[string]*desktopLogin{"browser-state": {claim: sha256.Sum256([]byte(secret)), expires: time.Now().Add(time.Minute)}}
	r := gin.New()
	r.POST("/claim", h.DesktopClaim)
	r.POST("/approve", h.DesktopApprove)
	call := func(path, body, contentType string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		r.ServeHTTP(w, req)
		return w
	}
	claim := func(secret string) *httptest.ResponseRecorder {
		return call("/claim", `{"claim":"`+secret+`"}`, "application/json")
	}
	if w := claim(secret); w.Code != 202 {
		t.Fatalf("pending: %d", w.Code)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/callback?state=browser-state", http.NoBody)
	if !h.finishDesktopLogin(c, "u1") {
		t.Fatal("desktop callback not handled")
	}
	if w := claim(secret); w.Code != 202 {
		t.Fatalf("unapproved: %d", w.Code)
	}
	if w := claim(strings.Repeat("b", 64)); w.Code != 410 {
		t.Fatalf("wrong secret: %d", w.Code)
	}
	if w := call("/approve", "state=browser-state", "application/x-www-form-urlencoded"); w.Code != 400 {
		t.Fatalf("missing approval secret: %d", w.Code)
	}
	if w := call("/approve", "state=browser-state&approval="+h.desktop["browser-state"].approval, "application/x-www-form-urlencoded"); w.Code != 200 {
		t.Fatalf("approve: %d", w.Code)
	}
	w := claim(secret)
	if w.Code != 204 || len(w.Result().Cookies()) == 0 {
		t.Fatalf("claim: %d, cookies %v", w.Code, w.Result().Cookies())
	}
	if w := claim(secret); w.Code != 410 {
		t.Fatalf("replayed: %d", w.Code)
	}
}

func TestDesktopClaimExpiresAndPropagatesErrors(t *testing.T) {
	h := NewOIDCHandler(nil, &config.Config{}, nil)
	secret := strings.Repeat("c", 64)
	h.desktop = map[string]*desktopLogin{"state": {claim: sha256.Sum256([]byte(secret)), expires: time.Now().Add(time.Minute)}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/callback?state=state", http.NoBody)
	if !h.desktopError(c, "access denied") {
		t.Fatal("missing desktop attempt")
	}
	r := gin.New()
	r.POST("/claim", h.DesktopClaim)
	req := httptest.NewRequest("POST", "/claim", strings.NewReader(`{"claim":"`+secret+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 401 || !strings.Contains(w.Body.String(), "access denied") {
		t.Fatalf("error: %d %s", w.Code, w.Body.String())
	}
	h.desktop["expired"] = &desktopLogin{claim: sha256.Sum256([]byte(secret)), expires: time.Now().Add(-time.Second)}
	req = httptest.NewRequest("POST", "/claim", strings.NewReader(`{"claim":"`+secret+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 410 {
		t.Fatalf("expired: %d", w.Code)
	}
}
