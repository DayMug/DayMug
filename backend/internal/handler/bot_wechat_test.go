package handler

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// setupWeChatPairingRouter points the pairing endpoints at a stub gateway.
func setupWeChatPairingRouter(ms *storetest.Fake, gateway string) *gin.Engine {
	r := gin.New()
	h := &BotHandler{Store: ms, Bots: ms, WeChatGateway: gateway}
	r.POST("/api/users/:id/bots/wechat-pairing", h.StartWeChatPairing)
	r.GET("/api/users/:id/bots/wechat-pairing", h.PollWeChatPairing)
	return r
}

func newAgentFake() *storetest.Fake {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "agent", OwnerID: "owner", Name: "Agent"}}
	return ms
}

func TestStartWeChatPairingReturnsChallengeAndImage(t *testing.T) {
	// The gateway returns a URL, not image bytes; the QR must be rendered here.
	const scanURL = "https://liteapp.weixin.qq.com/q/7GiQu1?qrcode=chal-1&bot_type=3"
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("bot_type"); got != "3" {
			t.Errorf("bot_type = %q, want 3", got)
		}
		_, _ = w.Write([]byte(`{"qrcode":"chal-1","qrcode_img_content":"` + scanURL + `"}`))
	}))
	defer gateway.Close()

	r := setupWeChatPairingRouter(newAgentFake(), gateway.URL)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/users/agent/bots/wechat-pairing", http.NoBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got wechatPairingStartResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Challenge != "chal-1" {
		t.Errorf("Challenge = %q", got.Challenge)
	}
	if got.QRContent != scanURL {
		t.Errorf("QRContent = %q, want the gateway's scan URL", got.QRContent)
	}
	decoded, err := base64.StdEncoding.DecodeString(got.QRImage)
	if err != nil {
		t.Fatalf("QRImage is not base64: %v", err)
	}
	if !bytes.HasPrefix(decoded, []byte("\x89PNG\r\n\x1a\n")) {
		t.Error("QRImage is not a rendered PNG")
	}
}

// Regression: the status endpoint long-polls for ~30s. A short client deadline
// made every poll look like a gateway failure, which reached the browser as a
// 502 before the user could even scan.
func TestPollWeChatPairingReportsSlowGatewayAsPending(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Answer "wait" only after a delay, the way the real gateway does.
		select {
		case <-r.Context().Done():
			return
		case <-time.After(150 * time.Millisecond):
		}
		_, _ = w.Write([]byte(`{"ret":0,"status":"wait"}`))
	}))
	defer gateway.Close()

	r := setupWeChatPairingRouter(newAgentFake(), gateway.URL)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(
		http.MethodGet, "/api/users/agent/bots/wechat-pairing?challenge=chal-1", http.NoBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 — a slow scan is not a gateway failure: %s", rec.Code, rec.Body.String())
	}
	var got wechatPairingStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" {
		t.Errorf("Status = %q, want pending", got.Status)
	}
}

func TestPollWeChatPairingMapsGatewayStatus(t *testing.T) {
	tests := []struct {
		name       string
		gatewayRsp string
		wantStatus string
		wantToken  string
		wantBase   string
	}{
		{
			name:       "pending",
			gatewayRsp: `{"status":"pending"}`,
			wantStatus: "pending",
		},
		{
			// An unrecognised status is far more likely to be a slow scan than
			// a dead challenge, so it must not send the user back to rescan.
			name:       "unknown status counts as pending",
			gatewayRsp: `{"status":"scanned_awaiting_confirm"}`,
			wantStatus: "pending",
		},
		{
			name:       "scanned but not yet confirmed",
			gatewayRsp: `{"status":"scaned"}`,
			wantStatus: "scanned",
		},
		{
			name:       "expired",
			gatewayRsp: `{"status":"expired"}`,
			wantStatus: "expired",
		},
		{
			// Retrying the same challenge cannot clear this, and DayMug has no
			// way to collect a code, so it must not read as a plain expiry.
			name:       "verification code required",
			gatewayRsp: `{"status":"need_verifycode"}`,
			wantStatus: "blocked",
		},
		{
			// A redirect means keep polling, not that the pairing failed.
			name:       "IDC redirect stays pending",
			gatewayRsp: `{"status":"scaned_but_redirect","redirect_host":"other.example"}`,
			wantStatus: "pending",
		},
		{
			name:       "confirmed yields both halves of the pairing",
			gatewayRsp: `{"status":"confirmed","bot_token":"tok","baseurl":"https://acct.example/"}`,
			wantStatus: "confirmed",
			wantToken:  "tok",
			wantBase:   "https://acct.example",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("qrcode"); got != "chal-1" {
					t.Errorf("qrcode = %q, want chal-1", got)
				}
				_, _ = w.Write([]byte(tc.gatewayRsp))
			}))
			defer gateway.Close()

			r := setupWeChatPairingRouter(newAgentFake(), gateway.URL)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(
				http.MethodGet, "/api/users/agent/bots/wechat-pairing?challenge=chal-1", http.NoBody))

			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			var got wechatPairingStatusResponse
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.BotToken != tc.wantToken {
				t.Errorf("BotToken = %q, want %q", got.BotToken, tc.wantToken)
			}
			if got.BaseURL != tc.wantBase {
				t.Errorf("BaseURL = %q, want %q", got.BaseURL, tc.wantBase)
			}
		})
	}
}

func TestPollWeChatPairingRequiresChallenge(t *testing.T) {
	r := setupWeChatPairingRouter(newAgentFake(), "https://unused.example")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/users/agent/bots/wechat-pairing", http.NoBody))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// Pairing must be scoped to an agent the caller owns, exactly like every other
// bot endpoint.
func TestWeChatPairingRejectsUnknownAgent(t *testing.T) {
	r := setupWeChatPairingRouter(storetest.New(), "https://unused.example")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/users/ghost/bots/wechat-pairing", http.NoBody))
	if rec.Code == http.StatusOK {
		t.Fatalf("pairing succeeded for an agent the caller does not own: %s", rec.Body.String())
	}
}
