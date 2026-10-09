package wechat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultLongPollTimeout must exceed the ~35s the gateway holds a
	// getUpdates request open, or every poll dies on a client-side abort
	// just before the server would have answered.
	DefaultLongPollTimeout = 45 * time.Second
	// DefaultAPITimeout covers the ordinary request/response endpoints.
	DefaultAPITimeout = 15 * time.Second
	// DefaultLightTimeout covers getConfig / sendTyping / notify*, which are
	// advisory: a slow one should be abandoned rather than delay a reply.
	DefaultLightTimeout = 10 * time.Second
)

// maxResponseBytes caps every response body. No legitimate JSON response from
// this gateway comes close; the cap exists so a wedged or hostile endpoint
// cannot make the poller allocate without bound.
const maxResponseBytes int64 = 8 << 20

// appID is the fixed iLink-App-Id the bot protocol expects. It is not a
// per-integration credential — it identifies the "bot" surface itself.
const appID = "bot"

// ClientOptions configures a Client. Token and BaseURL both come from a
// completed QR login; everything else has a working default.
type ClientOptions struct {
	// BaseURL is the per-account gateway returned by the login handshake.
	// Falls back to DefaultGateway only so that unauthenticated calls (QR
	// login) can share this transport.
	BaseURL string
	Token   string
	// BotAgent is a self-declared "Name/Version" string for server-side log
	// attribution. Sanitized before use; never affects auth or routing.
	BotAgent string
	// ChannelVersion is reported in base_info and encoded into the
	// iLink-App-ClientVersion header.
	ChannelVersion string
	// CDNBase overrides the media CDN. Empty means DefaultCDNBase; tests
	// point it at an httptest server.
	CDNBase    string
	HTTPClient *http.Client
	// Now is injectable so tests can drive time-dependent behaviour.
	Now func() time.Time
}

// Client speaks the iLink Bot HTTP API. It is safe for concurrent use.
type Client struct {
	baseURL        string
	token          string
	botAgent       string
	channelVersion string
	clientVersion  string
	cdnBase        string
	http           *http.Client
	now            func() time.Time
}

// NewClient builds a Client. It never fails: an empty BaseURL falls back to
// DefaultGateway so the QR-login endpoints work before an account exists.
func NewClient(opts ClientOptions) *Client {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = DefaultGateway
	}
	version := strings.TrimSpace(opts.ChannelVersion)
	if version == "" {
		version = "1.0.0"
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		// No global timeout: the long poll needs a far longer deadline than
		// the other endpoints, so each call sets its own via context.
		httpClient = &http.Client{}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		baseURL:        base,
		token:          strings.TrimSpace(opts.Token),
		botAgent:       SanitizeBotAgent(opts.BotAgent),
		channelVersion: version,
		clientVersion:  encodeClientVersion(version),
		cdnBase:        strings.TrimRight(strings.TrimSpace(opts.CDNBase), "/"),
		http:           httpClient,
		now:            now,
	}
}

// BaseURL reports the gateway this client talks to.
func (c *Client) BaseURL() string { return c.baseURL }

func (c *Client) baseInfo() *BaseInfo {
	return &BaseInfo{ChannelVersion: c.channelVersion, BotAgent: c.botAgent}
}

// encodeClientVersion packs a semver string into the uint32 the
// iLink-App-ClientVersion header expects: 0x00MMNNPP.
func encodeClientVersion(version string) string {
	var parts [3]int
	for i, raw := range strings.SplitN(version, ".", 3) {
		if i > 2 {
			break
		}
		// A non-numeric or suffixed component (e.g. "1.0.0-dev") degrades to
		// 0 rather than failing the request — the header is informational.
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < 0 {
			n = 0
		}
		parts[i] = n & 0xff
	}
	return strconv.Itoa(parts[0]<<16 | parts[1]<<8 | parts[2])
}

// randomWechatUin builds the X-WECHAT-UIN header: a fresh random uint32
// rendered as a decimal string, then base64-encoded. It changes per request
// as a replay guard, so it must not be cached.
func randomWechatUin() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failing is fatal for the process elsewhere; here a
		// constant is still a well-formed header and lets the call proceed.
		return base64.StdEncoding.EncodeToString([]byte("0"))
	}
	n := binary.BigEndian.Uint32(buf[:])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(n), 10)))
}

// newClientID generates the client_id echoed back on delivery, used to match
// an outbound send against its acknowledgement.
func newClientID() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "daymug"
	}
	return "daymug-" + hex.EncodeToString(buf[:])
}

func (c *Client) commonHeaders(h http.Header) {
	h.Set("iLink-App-Id", appID)
	h.Set("iLink-App-ClientVersion", c.clientVersion)
}

// APIError is a transport- or gateway-level failure carrying enough context to
// tell "the network broke" from "the gateway said no".
type APIError struct {
	Endpoint string
	Status   int
	Ret      int
	ErrCode  int
	Message  string
}

func (e *APIError) Error() string {
	switch {
	case e.Status != 0:
		return fmt.Sprintf("wechat %s: HTTP %d: %s", e.Endpoint, e.Status, e.Message)
	default:
		return fmt.Sprintf("wechat %s: ret=%d errcode=%d: %s", e.Endpoint, e.Ret, e.ErrCode, e.Message)
	}
}

// Permanent reports authentication and authorization rejections that cannot
// recover while the configured token and account permissions stay unchanged.
func (e *APIError) Permanent() bool {
	if e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden || e.ErrCode == ErrCodeSessionExpired {
		return true
	}
	message := strings.ToLower(e.Message)
	for _, marker := range []string{"unauthorized", "forbidden", "permission denied", "access denied", "no permission", "权限", "未授权"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// doJSON POSTs a JSON body and decodes the JSON response into out.
func (c *Client) doJSON(ctx context.Context, endpoint string, body any, out any, timeout time.Duration) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("wechat %s: encode request: %w", endpoint, err)
	}

	ctx, cancel := withTimeout(ctx, timeout)
	defer cancel()

	target, err := c.resolve(endpoint)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("wechat %s: build request: %w", endpoint, err)
	}
	logWire(endpoint, "request", payload)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("X-WECHAT-UIN", randomWechatUin())
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	c.commonHeaders(req.Header)

	return c.roundTrip(req, endpoint, out)
}

// doGet issues a GET carrying only the common headers. The QR-login endpoints
// are unauthenticated and take their arguments in the query string.
func (c *Client) doGet(ctx context.Context, endpoint string, out any, timeout time.Duration) error {
	ctx, cancel := withTimeout(ctx, timeout)
	defer cancel()

	target, err := c.resolve(endpoint)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("wechat %s: build request: %w", endpoint, err)
	}
	c.commonHeaders(req.Header)

	return c.roundTrip(req, endpoint, out)
}

func (c *Client) resolve(endpoint string) (string, error) {
	ref, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("wechat: bad endpoint %q: %w", endpoint, err)
	}
	base, err := url.Parse(c.baseURL + "/")
	if err != nil {
		return "", fmt.Errorf("wechat: bad base URL %q: %w", c.baseURL, err)
	}
	return base.ResolveReference(ref).String(), nil
}

func (c *Client) roundTrip(req *http.Request, endpoint string, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("wechat %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("wechat %s: read response: %w", endpoint, err)
	}
	logWire(endpoint, "response", raw)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Endpoint: endpoint, Status: resp.StatusCode, Message: truncate(string(raw), 512)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("wechat %s: decode response %q: %w", endpoint, truncate(string(raw), 256), err)
	}
	return nil
}

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

const (
	// botAgentMaxLen matches the gateway's documented cap.
	botAgentMaxLen  = 256
	defaultBotAgent = "DayMug"
)

// SanitizeBotAgent reduces a caller-supplied agent string to something safe to
// put on the wire: printable ASCII only, length-capped, never empty. The value
// is purely informational on the server, so anything unusable degrades to the
// default instead of failing the request.
func SanitizeBotAgent(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return defaultBotAgent
	}
	var b strings.Builder
	lastWasSpace := false
	for _, r := range trimmed {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f':
			// Collapse runs of whitespace so a multi-line value cannot
			// smuggle structure into the server's log line.
			if b.Len() > 0 && !lastWasSpace {
				b.WriteByte(' ')
				lastWasSpace = true
			}
		case r >= 0x21 && r <= 0x7e:
			b.WriteRune(r)
			lastWasSpace = false
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return defaultBotAgent
	}
	if len(out) > botAgentMaxLen {
		out = strings.TrimSpace(out[:botAgentMaxLen])
	}
	if out == "" {
		return defaultBotAgent
	}
	return out
}
