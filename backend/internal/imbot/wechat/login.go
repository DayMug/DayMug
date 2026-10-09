package wechat

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// QR login endpoints. bot_type=3 selects the third-party bot surface.
const (
	epGetQRCode       = "ilink/bot/get_bot_qrcode?bot_type=3"
	epGetQRCodeStatus = "ilink/bot/get_qrcode_status?qrcode="
)

// QR status values the gateway actually reports.
const (
	qrStatusWait              = "wait"
	qrStatusScanned           = "scaned" // gateway's spelling
	qrStatusConfirmed         = "confirmed"
	qrStatusExpired           = "expired"
	qrStatusScannedRedirect   = "scaned_but_redirect"
	qrStatusBindedRedirect    = "binded_redirect"
	qrStatusNeedVerifyCode    = "need_verifycode"
	qrStatusVerifyCodeBlocked = "verify_code_blocked"
)

// qrLongPollWindow is roughly how long the gateway holds a status request open
// before answering "wait". Callers must allow for it plus transport slack, or
// every poll dies on a client-side abort just before the answer arrives.
const qrLongPollWindow = 30 * time.Second

// QRCode is a pending login challenge.
type QRCode struct {
	// Code identifies this challenge when polling for its status.
	Code string `json:"qrcode"`
	// ImageContent is the string to encode into a QR code — despite the field
	// name it is a liteapp.weixin.qq.com URL, not image data.
	ImageContent string `json:"qrcode_img_content"`
	Ret          int    `json:"ret"`
	ErrMsg       string `json:"errmsg"`
}

// PNG renders ImageContent as a QR code image.
//
// The gateway's `qrcode_img_content` field is a URL, not image data — the name
// is misleading. Whoever scans it is sent to liteapp.weixin.qq.com to confirm
// the pairing, so the bytes have to be generated here rather than decoded.
func (q *QRCode) PNG(size int) ([]byte, error) {
	if q == nil || strings.TrimSpace(q.ImageContent) == "" {
		return nil, errors.New("wechat: QR response carried no content to encode")
	}
	if size <= 0 {
		size = 512
	}
	// Medium recovery is what Weixin's own clients use: enough redundancy for a
	// phone camera without inflating the module count past easy scanning.
	png, err := qrcode.Encode(strings.TrimSpace(q.ImageContent), qrcode.Medium, size)
	if err != nil {
		return nil, fmt.Errorf("wechat: render QR code: %w", err)
	}
	return png, nil
}

// QRStatus is one poll of a pending login.
type QRStatus struct {
	Status string `json:"status"`
	// BotToken and BaseURL are only populated once Status is "confirmed".
	BotToken string `json:"bot_token"`
	BaseURL  string `json:"baseurl"`
	Ret      int    `json:"ret"`
	ErrMsg   string `json:"errmsg"`
}

// Pairing phases reported by Phase.
const (
	QRPhasePending   = "pending"
	QRPhaseScanned   = "scanned"
	QRPhaseConfirmed = "confirmed"
	QRPhaseExpired   = "expired"
	// QRPhaseBlocked covers the verification-code states. Retrying the same
	// challenge cannot clear them, and DayMug has no way to collect a code, so
	// they are surfaced separately rather than hidden behind "expired".
	QRPhaseBlocked = "blocked"
)

// Phase collapses the gateway's raw status into the outcomes a caller can act
// on. Anything unrecognised counts as pending: an unseen status is far more
// likely to be a slow scan than a dead challenge, and giving up early would
// make the user rescan for no reason.
func (s *QRStatus) Phase() string {
	if s == nil {
		return QRPhasePending
	}
	switch strings.ToLower(strings.TrimSpace(s.Status)) {
	case qrStatusConfirmed:
		return QRPhaseConfirmed
	case qrStatusExpired:
		return QRPhaseExpired
	case qrStatusNeedVerifyCode, qrStatusVerifyCodeBlocked:
		return QRPhaseBlocked
	case qrStatusScanned:
		// Scanned but not yet confirmed on the phone. Worth showing so the
		// user knows the scan registered and only the tap is missing.
		return QRPhaseScanned
	case qrStatusWait, qrStatusScannedRedirect, qrStatusBindedRedirect:
		return QRPhasePending
	}
	return QRPhasePending
}

// Credentials extracts the pairing result. fallbackBaseURL is used when the
// gateway does not name one, which older gateways omit — the login gateway is
// then the account's gateway too.
func (s *QRStatus) Credentials(fallbackBaseURL string) (*Credentials, error) {
	if s.Phase() != QRPhaseConfirmed {
		return nil, errors.New("wechat: pairing is not confirmed yet")
	}
	if s.BotToken == "" {
		return nil, errors.New("wechat: login confirmed but no bot token was issued")
	}
	base := strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(fallbackBaseURL), "/")
	}
	return &Credentials{Token: s.BotToken, BaseURL: base}, nil
}

// Credentials is what a completed login yields. Both fields must be persisted:
// the token authenticates every later call and the base URL routes them to the
// account's assigned gateway, which is not the login gateway.
type Credentials struct {
	Token   string
	BaseURL string
}

// RequestQRCode starts a login and returns the challenge to display.
func (c *Client) RequestQRCode(ctx context.Context) (*QRCode, error) {
	out := &QRCode{}
	if err := c.doGet(ctx, epGetQRCode, out, DefaultAPITimeout); err != nil {
		return nil, err
	}
	if out.Ret != 0 {
		return nil, &APIError{Endpoint: epGetQRCode, Ret: out.Ret, Message: out.ErrMsg}
	}
	if out.Code == "" {
		return nil, errors.New("wechat: gateway returned an empty QR challenge")
	}
	return out, nil
}

// PollQRCode reads the state of a pending login exactly once.
//
// This endpoint long-polls: the gateway holds the request open for about
// qrLongPollWindow and only then answers "wait". A client deadline shorter
// than that turns every poll into a failure, so timeout defaults to the
// window plus transport slack, and hitting it is reported as "wait" rather
// than as an error — nothing went wrong, the user simply has not scanned yet.
func (c *Client) PollQRCode(ctx context.Context, code string, timeout time.Duration) (*QRStatus, error) {
	if timeout <= 0 {
		timeout = qrLongPollWindow + DefaultLightTimeout
	}
	out := &QRStatus{}
	if err := c.doGet(ctx, epGetQRCodeStatus+url.QueryEscape(code), out, timeout); err != nil {
		// Only our own deadline means "still waiting"; a cancelled parent
		// context is the caller giving up and must not be disguised.
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return &QRStatus{Status: qrStatusWait}, nil
		}
		return nil, err
	}
	if out.Ret != 0 {
		return nil, &APIError{Endpoint: epGetQRCodeStatus, Ret: out.Ret, Message: out.ErrMsg}
	}
	return out, nil
}

// ErrQRCodeExpired reports a challenge the user can no longer act on. The
// caller has to request a fresh QR code.
var ErrQRCodeExpired = errors.New("wechat: QR challenge expired or was declined")

// WaitForScanOptions tunes the login poll loop.
type WaitForScanOptions struct {
	// Interval paces retries after a *failed* poll. A successful poll already
	// blocked for the gateway's long-poll window, so it is re-issued at once.
	// Defaults to 2s.
	Interval time.Duration
	// Timeout bounds the whole wait. Defaults to 8 minutes, matching how long
	// the gateway keeps a challenge alive.
	Timeout time.Duration
	// OnPending, when set, is called after each inconclusive poll so a CLI
	// can show progress. Never called with a terminal status.
	OnPending func(status string)
}

// WaitForScan polls until the user confirms on their phone. Transient poll
// failures are tolerated — a flaky network mid-scan should not force the user
// to rescan — but a terminal status or the overall timeout ends the wait.
func (c *Client) WaitForScan(ctx context.Context, code string, opts WaitForScanOptions) (*Credentials, error) {
	interval := opts.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Minute
	}

	deadline := c.now().Add(timeout)

	var lastErr error
	for {
		failed := false
		status, err := c.PollQRCode(ctx, code, 0)
		switch {
		case err != nil:
			lastErr, failed = err, true
		case status.Phase() == QRPhaseConfirmed:
			return status.Credentials(c.baseURL)
		case status.Phase() == QRPhaseExpired:
			return nil, fmt.Errorf("%w (status %q)", ErrQRCodeExpired, status.Status)
		case status.Phase() == QRPhaseBlocked:
			return nil, fmt.Errorf("wechat: pairing needs a verification code this client cannot supply (status %q)", status.Status)
		default:
			lastErr = nil
			if opts.OnPending != nil {
				opts.OnPending(status.Status)
			}
		}

		if !c.now().Before(deadline) {
			if lastErr != nil {
				return nil, fmt.Errorf("wechat: login timed out, last error: %w", lastErr)
			}
			return nil, ErrQRCodeExpired
		}

		if !failed {
			// The poll already parked for the long-poll window; re-issue it
			// immediately instead of adding idle time on top.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}
