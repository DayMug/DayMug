package imbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The Bot API surface DayMug needs is nine methods of plain JSON-over-HTTPS, so
// it is spoken directly instead of pulling in an SDK: no dependency owns our
// retry, timeout, or teardown semantics, all of which the connector supervisor
// already dictates.
const (
	telegramAPIBase = "https://api.telegram.org"
	// telegramPollTimeout is the server-side long-poll window. Telegram holds
	// the request open this long when no update is pending, which is what makes
	// polling as cheap as a socket without needing a public endpoint.
	telegramPollTimeout = 50 * time.Second
	// telegramCallTimeout bounds an ordinary (non-polling) call, and is also
	// the slack the poll request gets on top of its server-side window.
	telegramCallTimeout = 30 * time.Second
	// telegramPollLimit caps updates per getUpdates round trip.
	telegramPollLimit = 100
	// telegramMaxResponseBytes caps a decoded API response. Only file downloads
	// are large, and those stream to the caller's writer instead.
	telegramMaxResponseBytes = 8 << 20
	// telegramMaxRetryAfter is the longest 429 backoff worth honouring inline;
	// past it the caller's own retry (or the supervisor) does better.
	telegramMaxRetryAfter = 30 * time.Second
)

// telegramClient talks to one bot's Bot API endpoint.
type telegramClient struct {
	token   string
	baseURL string
	http    *http.Client
}

func newTelegramClient(token string) *telegramClient {
	return &telegramClient{
		token:   token,
		baseURL: telegramAPIBase,
		// Deliberately no client-level timeout: getUpdates parks for
		// telegramPollTimeout by design, and every call already carries a
		// context deadline that also aborts the in-flight request on Stop.
		http: &http.Client{},
	}
}

// telegramError is a structured Bot API rejection. Callers branch on Code to
// tell a revoked token (401) or a competing consumer (409) — both permanent —
// apart from transient failures, and on RetryAfter to honour 429 throttling.
type telegramError struct {
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *telegramError) Error() string {
	return fmt.Sprintf("telegram api %d: %s", e.Code, e.Description)
}

// Permanent reports whether retrying the same call can ever succeed. A revoked
// or malformed token and a conflicting getUpdates consumer (another process
// polling, or a webhook still registered) both stay broken until a human acts,
// so the poll loop surfaces them immediately instead of burning the retry
// budget silently.
func (e *telegramError) Permanent() bool {
	return e.Code == http.StatusUnauthorized || e.Code == http.StatusNotFound || e.Code == http.StatusConflict
}

type telegramEnvelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// call invokes one Bot API method, decoding "result" into out (which may be
// nil when the result is uninteresting). A 429 is retried once after the
// server-dictated delay: the bridge throttles progress edits to 3s, but a
// long reply's chunk burst can still trip the per-chat limit, and losing the
// final answer to a recoverable throttle would be the worst outcome.
func (c *telegramClient) call(ctx context.Context, method string, payload, out any) error {
	for attempt := 0; ; attempt++ {
		err := c.callOnce(ctx, method, payload, out)
		var apiErr *telegramError
		if attempt > 0 || !errors.As(err, &apiErr) || apiErr.RetryAfter <= 0 || apiErr.RetryAfter > telegramMaxRetryAfter {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(apiErr.RetryAfter):
		}
	}
}

func (c *telegramClient) callOnce(ctx context.Context, method string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram %s: encode request: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(method), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, method, out)
}

func (c *telegramClient) do(req *http.Request, method string, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, telegramMaxResponseBytes))
	if err != nil {
		return fmt.Errorf("telegram %s: read response: %w", method, err)
	}
	var envelope telegramEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("telegram %s: decode response (http %d): %w", method, resp.StatusCode, err)
	}
	if !envelope.OK {
		apiErr := &telegramError{Code: envelope.ErrorCode, Description: envelope.Description}
		if apiErr.Code == 0 {
			apiErr.Code = resp.StatusCode
		}
		if envelope.Parameters != nil && envelope.Parameters.RetryAfter > 0 {
			apiErr.RetryAfter = time.Duration(envelope.Parameters.RetryAfter) * time.Second
		}
		return apiErr
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("telegram %s: decode result: %w", method, err)
	}
	return nil
}

func (c *telegramClient) methodURL(method string) string {
	return strings.TrimRight(c.baseURL, "/") + "/bot" + c.token + "/" + method
}

// upload posts a multipart form, used by the file-sending methods that carry
// bytes rather than a JSON body.
func (c *telegramClient) upload(ctx context.Context, method string, fields map[string]string, fileField, fileName string, content io.Reader, out any) error {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := form.WriteField(name, value); err != nil {
			return fmt.Errorf("telegram %s: write field %q: %w", method, name, err)
		}
	}
	part, err := form.CreateFormFile(fileField, fileName)
	if err != nil {
		return fmt.Errorf("telegram %s: create file part: %w", method, err)
	}
	if _, err := io.Copy(part, content); err != nil {
		return fmt.Errorf("telegram %s: buffer %q: %w", method, fileName, err)
	}
	if err := form.Close(); err != nil {
		return fmt.Errorf("telegram %s: close form: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(method), &body)
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	return c.do(req, method, out)
}

// download streams a previously resolved file path to dst. Bot API downloads
// are capped at 20 MiB server-side; the bridge additionally hands us a
// size-limited writer, so nothing here needs its own budget.
func (c *telegramClient) download(ctx context.Context, filePath string, dst io.Writer) error {
	if strings.TrimSpace(filePath) == "" {
		return errors.New("telegram download: empty file path")
	}
	endpoint := strings.TrimRight(c.baseURL, "/") + "/file/bot" + c.token + "/" + escapeTelegramFilePath(filePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("telegram download: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram download: http %d", resp.StatusCode)
	}
	if _, err := io.Copy(dst, resp.Body); err != nil {
		return fmt.Errorf("telegram download: %w", err)
	}
	return nil
}

// escapeTelegramFilePath percent-encodes each path segment. Telegram returns
// file paths like "photos/file_1.jpg"; the separators must survive while any
// exotic character in a user-supplied filename must not break the URL.
func escapeTelegramFilePath(filePath string) string {
	segments := strings.Split(filePath, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// --- Bot API payload types (only the fields DayMug reads) ---

type telegramUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
	// CanJoinGroups and CanReadAllGroupMessages are only populated by getMe
	// called with the bot's own token; they report the @BotFather switches the
	// connection test surfaces.
	CanJoinGroups           bool `json:"can_join_groups"`
	CanReadAllGroupMessages bool `json:"can_read_all_group_messages"`
}

func (u *telegramUser) displayName() string {
	if u == nil {
		return ""
	}
	full := strings.TrimSpace(strings.TrimSpace(u.FirstName) + " " + strings.TrimSpace(u.LastName))
	return firstNonEmpty(full, u.Username)
}

type telegramChat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username"`
}

// telegramEntity carries the parsed markup of a message. DayMug only reads
// text_mention entities (a mention of a user without a public username, which
// no substring scan could find).
type telegramEntity struct {
	Type string        `json:"type"`
	User *telegramUser `json:"user"`
}

// telegramFile is the shape every non-photo attachment shares. Voice notes
// carry no file_name, which the connector substitutes for.
type telegramFile struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type telegramPhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	FileSize int64  `json:"file_size"`
}

type telegramMessage struct {
	MessageID       int64            `json:"message_id"`
	MessageThreadID int64            `json:"message_thread_id"`
	IsTopicMessage  bool             `json:"is_topic_message"`
	From            *telegramUser    `json:"from"`
	Chat            *telegramChat    `json:"chat"`
	Text            string           `json:"text"`
	Caption         string           `json:"caption"`
	Entities        []telegramEntity `json:"entities"`
	CaptionEntities []telegramEntity `json:"caption_entities"`
	ReplyToMessage  *telegramMessage `json:"reply_to_message"`

	Document *telegramFile       `json:"document"`
	Video    *telegramFile       `json:"video"`
	Audio    *telegramFile       `json:"audio"`
	Voice    *telegramFile       `json:"voice"`
	Photo    []telegramPhotoSize `json:"photo"`
}

// body returns the message's human text, which lives in caption when the
// message carries a file.
func (m *telegramMessage) body() string {
	return firstNonEmpty(m.Text, m.Caption)
}

func (m *telegramMessage) entities() []telegramEntity {
	if len(m.Entities) > 0 {
		return m.Entities
	}
	return m.CaptionEntities
}

type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message"`
}
