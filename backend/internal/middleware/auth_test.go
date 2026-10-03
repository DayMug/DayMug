package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// probeBody is what the terminal handler of every test route reports, so a
// test can assert both "the request got through" and "what the middleware put
// into the context" from a single response.
type probeBody struct {
	UserID  string `json:"user_id"`
	IsAdmin bool   `json:"is_admin"`
	Reached bool   `json:"reached"`
}

// newTestRouter wires the middleware under test in front of a probe handler.
func newTestRouter(t *testing.T, mw ...gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	handlers := append([]gin.HandlerFunc{}, mw...)
	handlers = append(handlers, func(c *gin.Context) {
		c.JSON(http.StatusOK, probeBody{
			UserID:  CurrentUserID(c),
			IsAdmin: CurrentUserIsAdmin(c),
			Reached: true,
		})
	})
	r.GET("/probe", handlers...)
	return r
}

func doGET(r *gin.Engine, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/probe", http.NoBody)
	if mutate != nil {
		mutate(req)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func withCookie(token string) func(*http.Request) {
	return func(req *http.Request) {
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	}
}

func decodeProbe(t *testing.T, w *httptest.ResponseRecorder) probeBody {
	t.Helper()
	var body probeBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode probe body %q: %v", w.Body.String(), err)
	}
	return body
}

// errStore lets a test inject transport-level failures the pure in-memory Fake
// can't produce (RequireAuth's 500 branch).
type errStore struct {
	*storetest.Fake
	sessionErr error
}

func (s *errStore) GetSession(ctx context.Context, token string) (store.Session, error) {
	if s.sessionErr != nil {
		return store.Session{}, s.sessionErr
	}
	return s.Fake.GetSession(ctx, token)
}

func seedSession(f *storetest.Fake, token string, user store.User) {
	f.Users = append(f.Users, user)
	f.Sessions[token] = store.Session{
		Token:     token,
		UserID:    user.ID,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

func TestRequireAuthRejectsMissingCookie(t *testing.T) {
	r := newTestRouter(t, RequireAuth(storetest.New()))

	w := doGET(r, nil)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestRequireAuthRejectsEmptyCookieValue(t *testing.T) {
	r := newTestRouter(t, RequireAuth(storetest.New()))

	w := doGET(r, withCookie(""))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestRequireAuthRejectsUnknownSession(t *testing.T) {
	r := newTestRouter(t, RequireAuth(storetest.New()))

	w := doGET(r, withCookie("nope"))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// Expiry is enforced inside the store (SQLiteStore.GetSession maps an elapsed
// expires_at to ErrNotFound), so from the middleware's side an expired session
// is indistinguishable from a deleted one; both must yield 401.
func TestRequireAuthRejectsExpiredSession(t *testing.T) {
	s := &errStore{Fake: storetest.New(), sessionErr: store.ErrNotFound}
	r := newTestRouter(t, RequireAuth(s))

	w := doGET(r, withCookie("expired"))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestRequireAuthStoreFailureIsServerError(t *testing.T) {
	s := &errStore{Fake: storetest.New(), sessionErr: errors.New("db is down")}
	r := newTestRouter(t, RequireAuth(s))

	w := doGET(r, withCookie("tok"))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

func TestRequireAuthRejectsSessionWithMissingUser(t *testing.T) {
	f := storetest.New()
	f.Sessions["orphan"] = store.Session{Token: "orphan", UserID: "ghost", ExpiresAt: time.Now().Add(time.Hour)}
	r := newTestRouter(t, RequireAuth(f))

	w := doGET(r, withCookie("orphan"))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestRequireAuthRejectsDisabledUser(t *testing.T) {
	f := storetest.New()
	seedSession(f, "tok", store.User{ID: "u1", Username: "bob", Disabled: true})
	r := newTestRouter(t, RequireAuth(f))

	w := doGET(r, withCookie("tok"))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestRequireAuthInjectsUserIntoContext(t *testing.T) {
	cases := []struct {
		name    string
		user    store.User
		isAdmin bool
	}{
		{name: "regular user", user: store.User{ID: "u1", Username: "bob"}},
		{name: "admin user", user: store.User{ID: "u2", Username: "root", IsAdmin: true}, isAdmin: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := storetest.New()
			seedSession(f, "tok", tc.user)
			r := newTestRouter(t, RequireAuth(f))

			w := doGET(r, withCookie("tok"))

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
			}
			body := decodeProbe(t, w)
			if body.UserID != tc.user.ID {
				t.Errorf("user id = %q, want %q", body.UserID, tc.user.ID)
			}
			if body.IsAdmin != tc.isAdmin {
				t.Errorf("is_admin = %v, want %v", body.IsAdmin, tc.isAdmin)
			}
		})
	}
}

func TestRequireAdminRejectsUnauthenticatedRequest(t *testing.T) {
	r := newTestRouter(t, RequireAdmin())

	w := doGET(r, nil)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestRequireAdminRejectsNonAdmin(t *testing.T) {
	f := storetest.New()
	seedSession(f, "tok", store.User{ID: "u1", Username: "bob"})
	r := newTestRouter(t, RequireAuth(f), RequireAdmin())

	w := doGET(r, withCookie("tok"))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestRequireAdminAllowsAdmin(t *testing.T) {
	f := storetest.New()
	seedSession(f, "tok", store.User{ID: "u2", Username: "root", IsAdmin: true})
	r := newTestRouter(t, RequireAuth(f), RequireAdmin())

	w := doGET(r, withCookie("tok"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if !decodeProbe(t, w).Reached {
		t.Error("handler was not reached")
	}
}

// RequireAdmin reads only the context flag, so an unauthenticated request must
// never slip through on a stale or absent key.
func TestRequireAdminIgnoresUserIDWithoutAdminFlag(t *testing.T) {
	r := newTestRouter(t, func(c *gin.Context) {
		c.Set(contextUserIDKey, "u1")
		c.Next()
	}, RequireAdmin())

	w := doGET(r, nil)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestCurrentUserIsAdmin(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*gin.Context)
		want  bool
	}{
		{name: "no auth middleware ran", setup: func(*gin.Context) {}},
		{name: "regular user", setup: func(c *gin.Context) { c.Set(contextIsAdminKey, false) }},
		{name: "admin user", setup: func(c *gin.Context) { c.Set(contextIsAdminKey, true) }, want: true},
		// Defensive: a non-bool value under the key must not be read as admin.
		{name: "non-bool value", setup: func(c *gin.Context) { c.Set(contextIsAdminKey, "true") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			tc.setup(c)

			if got := CurrentUserIsAdmin(c); got != tc.want {
				t.Errorf("CurrentUserIsAdmin() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCurrentUserID(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*gin.Context)
		want  string
	}{
		{name: "no auth middleware ran", setup: func(*gin.Context) {}},
		{name: "authenticated", setup: func(c *gin.Context) { c.Set(contextUserIDKey, "u1") }, want: "u1"},
		{name: "non-string value", setup: func(c *gin.Context) { c.Set(contextUserIDKey, 42) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			tc.setup(c)

			if got := CurrentUserID(c); got != tc.want {
				t.Errorf("CurrentUserID() = %q, want %q", got, tc.want)
			}
		})
	}
}
