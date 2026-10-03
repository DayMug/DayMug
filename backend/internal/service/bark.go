package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// BarkSender posts a notification to a Bark server. Implementations may be
// swapped out in tests; the package-level DefaultBarkSender hits the real
// Bark API over HTTP.
type BarkSender interface {
	// clickURL, when non-empty, is set as Bark's `url` field so tapping the
	// notification opens that link in the phone's browser. Pass "" to send a
	// notification with no tap target.
	Send(ctx context.Context, barkURL, title, body, clickURL string) error
}

type httpBarkSender struct {
	client *http.Client
}

// NewHTTPBarkSender returns a BarkSender backed by net/http with a sane
// default timeout. The Bark server is expected to be reachable from the
// machine running daymug.
func NewHTTPBarkSender() BarkSender {
	return &httpBarkSender{client: &http.Client{Timeout: 10 * time.Second}}
}

// Send POSTs a JSON payload to the user-supplied Bark URL. Bark accepts
// `{"title":"...","body":"..."}` at e.g. `https://api.day.app/<key>`. We
// trim trailing slashes to avoid double-slash variants from breaking on
// stricter Bark deployments.
func (s *httpBarkSender) Send(ctx context.Context, barkURL, title, body, clickURL string) error {
	url := strings.TrimRight(strings.TrimSpace(barkURL), "/")
	if url == "" {
		return fmt.Errorf("bark url is empty")
	}

	payload := map[string]string{
		"title": title,
		"body":  body,
	}
	if clickURL != "" {
		// Bark opens this URL when the notification is tapped.
		payload["url"] = clickURL
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal bark payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build bark request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("send bark request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		// Drain a small slice of the body to surface server-side errors in logs.
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("bark returned %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}
	return nil
}
