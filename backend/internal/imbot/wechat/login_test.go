package wechat

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRequestQRCodeReturnsChallenge(t *testing.T) {
	// The gateway's qrcode_img_content is a URL despite the field name.
	const scanURL = "https://liteapp.weixin.qq.com/q/7GiQu1?qrcode=abc123&bot_type=3"
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if got := r.URL.Query().Get("bot_type"); got != "3" {
			t.Errorf("bot_type = %q, want 3", got)
		}
		_, _ = w.Write([]byte(`{"qrcode":"abc123","qrcode_img_content":"` + scanURL + `"}`))
	})

	qr, err := client.RequestQRCode(context.Background())
	if err != nil {
		t.Fatalf("RequestQRCode: %v", err)
	}
	if qr.Code != "abc123" {
		t.Errorf("Code = %q, want abc123", qr.Code)
	}
	if qr.ImageContent != scanURL {
		t.Errorf("ImageContent = %q, want the scan URL verbatim", qr.ImageContent)
	}
}

// The content has to be encoded into a QR, not decoded as image bytes.
func TestQRCodePNGRendersTheScanURL(t *testing.T) {
	qr := &QRCode{ImageContent: "https://liteapp.weixin.qq.com/q/7GiQu1?qrcode=abc&bot_type=3"}
	got, err := qr.PNG(256)
	if err != nil {
		t.Fatalf("PNG: %v", err)
	}
	if !bytes.HasPrefix(got, []byte("\x89PNG\r\n\x1a\n")) {
		t.Errorf("PNG() did not return a PNG (first bytes %q)", got[:min(8, len(got))])
	}
}

func TestQRCodePNGRejectsEmptyContent(t *testing.T) {
	if _, err := (&QRCode{}).PNG(0); err == nil {
		t.Error("PNG() accepted an empty challenge")
	}
}

func TestWaitForScanReturnsCredentialsOnConfirm(t *testing.T) {
	polls := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("qrcode"); got != "abc123" {
			t.Errorf("qrcode = %q, want abc123", got)
		}
		polls++
		if polls < 3 {
			_, _ = w.Write([]byte(`{"status":"pending"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"confirmed","bot_token":"tok","baseurl":"https://acct.example/"}`))
	})

	pending := 0
	creds, err := client.WaitForScan(context.Background(), "abc123", WaitForScanOptions{
		Interval:  time.Millisecond,
		Timeout:   5 * time.Second,
		OnPending: func(string) { pending++ },
	})
	if err != nil {
		t.Fatalf("WaitForScan: %v", err)
	}
	if creds.Token != "tok" {
		t.Errorf("Token = %q, want tok", creds.Token)
	}
	// The per-account gateway differs from the login gateway and its trailing
	// slash must be normalised away before it is used as a base URL.
	if creds.BaseURL != "https://acct.example" {
		t.Errorf("BaseURL = %q, want https://acct.example", creds.BaseURL)
	}
	if pending != 2 {
		t.Errorf("OnPending called %d times, want 2", pending)
	}
}

func TestWaitForScanFallsBackToLoginGatewayWhenBaseURLOmitted(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"confirmed","bot_token":"tok"}`))
	})

	creds, err := client.WaitForScan(context.Background(), "abc", WaitForScanOptions{Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("WaitForScan: %v", err)
	}
	if creds.BaseURL != client.BaseURL() {
		t.Errorf("BaseURL = %q, want the login gateway %q", creds.BaseURL, client.BaseURL())
	}
}

func TestWaitForScanStopsOnExpiry(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"expired"}`))
	})
	_, err := client.WaitForScan(context.Background(), "abc", WaitForScanOptions{Interval: time.Millisecond})
	if !errors.Is(err, ErrQRCodeExpired) {
		t.Fatalf("err = %v, want ErrQRCodeExpired", err)
	}
}

// A verification-code challenge cannot be answered by this client, so looping
// on it would hang the pairing until the overall deadline.
func TestWaitForScanStopsWhenVerificationIsRequired(t *testing.T) {
	for _, status := range []string{"need_verifycode", "verify_code_blocked"} {
		t.Run(status, func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"status":"` + status + `"}`))
			})
			_, err := client.WaitForScan(context.Background(), "abc",
				WaitForScanOptions{Interval: time.Millisecond, Timeout: 5 * time.Second})
			if err == nil {
				t.Fatal("WaitForScan kept polling a challenge it can never satisfy")
			}
			if !strings.Contains(err.Error(), "verification code") {
				t.Errorf("err = %v, want it to name the verification code", err)
			}
		})
	}
}

func TestQRStatusPhase(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"wait", QRPhasePending},
		{"scaned", QRPhaseScanned},
		{"confirmed", QRPhaseConfirmed},
		{"expired", QRPhaseExpired},
		{"need_verifycode", QRPhaseBlocked},
		{"verify_code_blocked", QRPhaseBlocked},
		// Redirect states mean polling should continue, not that it failed.
		{"scaned_but_redirect", QRPhasePending},
		{"binded_redirect", QRPhasePending},
		// An unseen status is far more likely a slow scan than a dead code.
		{"something_new", QRPhasePending},
		{"", QRPhasePending},
	}
	for _, tc := range tests {
		t.Run(tc.status, func(t *testing.T) {
			if got := (&QRStatus{Status: tc.status}).Phase(); got != tc.want {
				t.Errorf("Phase(%q) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// The gateway holds a status request open for ~30s before answering "wait".
// Treating that deadline as an error is what made the browser see a 502.
func TestPollQRCodeReportsClientTimeoutAsWaiting(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(300 * time.Millisecond):
		}
	})

	got, err := client.PollQRCode(context.Background(), "abc", 30*time.Millisecond)
	if err != nil {
		t.Fatalf("PollQRCode: %v, want a waiting status instead of an error", err)
	}
	if got.Phase() != QRPhasePending {
		t.Errorf("Phase() = %q, want pending", got.Phase())
	}
}

// A cancelled caller is not the same as "still waiting" and must surface.
func TestPollQRCodeSurfacesCallerCancellation(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(300 * time.Millisecond):
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := client.PollQRCode(ctx, "abc", 5*time.Second); err == nil {
		t.Fatal("PollQRCode hid the caller's cancellation")
	}
}

// A flaky network mid-scan should not force the user to rescan.
func TestWaitForScanToleratesTransientPollFailures(t *testing.T) {
	polls := 0
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		polls++
		if polls < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"status":"confirmed","bot_token":"tok"}`))
	})

	creds, err := client.WaitForScan(context.Background(), "abc", WaitForScanOptions{Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("WaitForScan: %v", err)
	}
	if creds.Token != "tok" {
		t.Errorf("Token = %q, want tok", creds.Token)
	}
}

func TestWaitForScanRejectsConfirmWithoutToken(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"confirmed"}`))
	})
	_, err := client.WaitForScan(context.Background(), "abc", WaitForScanOptions{Interval: time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "no bot token") {
		t.Fatalf("err = %v, want a missing-token failure", err)
	}
}

func TestWaitForScanHonoursContextCancellation(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"pending"}`))
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := client.WaitForScan(ctx, "abc", WaitForScanOptions{Interval: 5 * time.Millisecond, Timeout: time.Minute})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
