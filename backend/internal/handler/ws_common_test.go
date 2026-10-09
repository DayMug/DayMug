package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAcceptWSOriginCheck(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		host      string
		origin    string
		fwdHost   string
		wantAllow bool
	}{
		{"no origin (non-browser client)", "daymug.example.com", "", "", true},
		{"same host", "daymug.example.com", "https://daymug.example.com", "", true},
		{"sibling subdomain", "daymug.example.com", "https://evil.example.com", "", false},
		{"foreign site", "daymug.example.com", "https://attacker.test", "", false},
		{"proxy rewrote Host, X-Forwarded-Host matches", "127.0.0.2:8090", "https://daymug.example.com", "daymug.example.com", true},
		{"X-Forwarded-Host does not match origin", "10.0.0.5:8090", "https://evil.example.com", "daymug.example.com", false},
		{"vite dev proxy on loopback", "localhost:8080", "http://localhost:5173", "", true},
		{"loopback origin against a public host", "daymug.example.com", "http://localhost:5173", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/api/ws", nil)
			r.Host = tc.host
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("Sec-WebSocket-Version", "13")
			r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.fwdHost != "" {
				r.Header.Set("X-Forwarded-Host", tc.fwdHost)
			}
			// The recorder can't be hijacked, so an admitted origin still fails
			// the upgrade — but with 500, after the origin check. Only a
			// rejected origin produces 403.
			w := httptest.NewRecorder()
			_, _, _, _ = acceptWS(w, r, "test")
			rejected := w.Code == http.StatusForbidden
			if rejected == tc.wantAllow {
				t.Fatalf("origin %q host %q fwd %q: status %d, want allow=%v", tc.origin, tc.host, tc.fwdHost, w.Code, tc.wantAllow)
			}
		})
	}
}
