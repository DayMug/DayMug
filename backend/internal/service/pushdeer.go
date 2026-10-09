package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PushDeerSender posts a notification to the PushDeer push API. Implementations
// may be swapped out in tests; production wires httpPushDeerSender, which hits
// https://api2.pushdeer.com/message/push with the user's pushkey.
type PushDeerSender interface {
	Send(ctx context.Context, pushKey, title, body string) error
}

type httpPushDeerSender struct {
	client   *http.Client
	endpoint string
}

// DefaultPushDeerEndpoint is the public PushDeer relay used when the
// user supplies just a pushkey. A self-hosted server can be added later by
// extending the sender to accept the endpoint per-call.
const DefaultPushDeerEndpoint = "https://api2.pushdeer.com/message/push"

// NewHTTPPushDeerSender returns a PushDeerSender backed by net/http hitting
// DefaultPushDeerEndpoint with a sane default timeout.
func NewHTTPPushDeerSender() PushDeerSender {
	return &httpPushDeerSender{
		client:   &http.Client{Timeout: 10 * time.Second},
		endpoint: DefaultPushDeerEndpoint,
	}
}

// Send posts the notification to PushDeer using form-encoded parameters as
// documented at https://www.pushdeer.com/dev.html — `pushkey` plus `text`
// (the headline) and an optional `desp` (the body). We keep `type=text` so
// the long body renders as plain text instead of trying to be markdown.
func (s *httpPushDeerSender) Send(ctx context.Context, pushKey, title, body string) error {
	key := strings.TrimSpace(pushKey)
	if key == "" {
		return fmt.Errorf("pushdeer key is empty")
	}

	form := url.Values{}
	form.Set("pushkey", key)
	form.Set("text", title)
	if body != "" {
		form.Set("desp", body)
	}
	form.Set("type", "text")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build pushdeer request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("send pushdeer request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("pushdeer returned %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}

	// PushDeer always returns 200 — even for an unknown key — so we have to
	// inspect the JSON envelope to surface auth/quota failures. Shape:
	// `{"code":0,"content":{...}}` on success, `{"code":!=0,"error":"..."}`
	// otherwise. We tolerate decode failures (e.g. a future field shape) so
	// a successful delivery on an evolving API isn't reported as an error.
	var envelope struct {
		Code  int    `json:"code"`
		Error string `json:"error"`
	}
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if len(buf) > 0 && json.Unmarshal(buf, &envelope) == nil && envelope.Code != 0 {
		msg := envelope.Error
		if msg == "" {
			msg = strings.TrimSpace(string(buf))
		}
		return fmt.Errorf("pushdeer error code=%d: %s", envelope.Code, msg)
	}
	return nil
}
