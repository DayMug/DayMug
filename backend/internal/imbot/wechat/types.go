// Package wechat implements the iLink Bot protocol that Weixin exposes to
// third-party bots (the same wire protocol Tencent's openclaw-weixin plugin
// speaks, reimplemented in Go).
//
// The protocol is an outbound long-poll: the client scans a QR code to trade a
// phone confirmation for a bot token, then holds a getUpdates request open
// until the gateway has messages. Nothing here needs a public callback URL,
// which is why it fits alongside DayMug's Slack Socket Mode and Feishu
// long-connection connectors rather than the webhook-shaped Weixin APIs.
//
// This package is deliberately standalone — it knows about the wire format and
// nothing about DayMug's imbot.Connector abstraction, so the protocol can be
// exercised and verified on its own before anything is wired into the bridge.
package wechat

// DefaultGateway is where QR login starts. Every other call goes to the
// per-account base URL the gateway hands back on a confirmed login, so this
// constant must not be used as the base URL for an authenticated client.
const DefaultGateway = "https://ilinkai.weixin.qq.com"

// Message type — who authored a message.
const (
	MessageTypeNone = 0
	MessageTypeUser = 1
	MessageTypeBot  = 2
)

// Message state. The gateway has no edit-in-place API: a bot's outbound
// message is always sent as StateFinish. StateGenerating appears on inbound
// messages the gateway itself is still streaming.
const (
	StateNew        = 0
	StateGenerating = 1
	StateFinish     = 2
)

// Message item types. TOOL_CALL_START / TOOL_CALL_RESULT are the protocol's
// native substitute for editing a progress message: the Weixin client folds
// every item sharing a run_id into one collapsible progress block.
const (
	ItemTypeNone           = 0
	ItemTypeText           = 1
	ItemTypeImage          = 2
	ItemTypeVoice          = 3
	ItemTypeFile           = 4
	ItemTypeVideo          = 5
	ItemTypeToolCallStart  = 11
	ItemTypeToolCallResult = 12
)

// Typing indicator states for SendTyping.
const (
	TypingActive = 1
	TypingCancel = 2
)

// ErrCodeSessionExpired is returned by the gateway once the bot token is no
// longer valid. It is not retryable: the account has to be re-paired by
// scanning a fresh QR code, so callers must stop hammering getUpdates.
const ErrCodeSessionExpired = -14

// BaseInfo rides along with every request. bot_agent is a self-declared
// User-Agent equivalent used for server-side log attribution only.
type BaseInfo struct {
	ChannelVersion string `json:"channel_version,omitempty"`
	BotAgent       string `json:"bot_agent,omitempty"`
}

// CDNMedia references an encrypted blob on the Weixin CDN. AESKey is
// base64-encoded on the wire.
type CDNMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
	FullURL           string `json:"full_url,omitempty"`
}

type TextItem struct {
	Text string `json:"text,omitempty"`
}

type ImageItem struct {
	Media     *CDNMedia `json:"media,omitempty"`
	ThumbMdia *CDNMedia `json:"thumb_media,omitempty"`
	// AESKey is the raw AES-128 key as a hex string. The gateway prefers it
	// over Media.AESKey for inbound decryption.
	AESKey  string `json:"aeskey,omitempty"`
	URL     string `json:"url,omitempty"`
	MidSize int    `json:"mid_size,omitempty"`
}

type VoiceItem struct {
	Media    *CDNMedia `json:"media,omitempty"`
	PlayTime int       `json:"playtime,omitempty"`
	// Text is the gateway's own transcription. It saves the client from
	// having to decode Weixin's silk audio codec.
	Text string `json:"text,omitempty"`
}

type FileItem struct {
	Media    *CDNMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	MD5      string    `json:"md5,omitempty"`
	Len      string    `json:"len,omitempty"`
}

type VideoItem struct {
	Media      *CDNMedia `json:"media,omitempty"`
	VideoSize  int       `json:"video_size,omitempty"`
	PlayLength int       `json:"play_length,omitempty"`
}

type ToolCallStartItem struct {
	ToolName   string `json:"tool_name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type ToolCallResultItem struct {
	ToolName   string `json:"tool_name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Status     string `json:"status,omitempty"`
}

// MessageItem is one piece of a message. A message carries a list of them, but
// outbound sends use exactly one item per request — the gateway rejects mixed
// item lists on some item types, and the plugin this was derived from splits
// captions into their own message for the same reason.
type MessageItem struct {
	Type         int    `json:"type,omitempty"`
	CreateTimeMS int64  `json:"create_time_ms,omitempty"`
	UpdateTimeMS int64  `json:"update_time_ms,omitempty"`
	IsCompleted  bool   `json:"is_completed,omitempty"`
	MsgID        string `json:"msg_id,omitempty"`

	TextItem  *TextItem  `json:"text_item,omitempty"`
	ImageItem *ImageItem `json:"image_item,omitempty"`
	VoiceItem *VoiceItem `json:"voice_item,omitempty"`
	FileItem  *FileItem  `json:"file_item,omitempty"`
	VideoItem *VideoItem `json:"video_item,omitempty"`

	ToolCallStartItem  *ToolCallStartItem  `json:"tool_call_start_item,omitempty"`
	ToolCallResultItem *ToolCallResultItem `json:"tool_call_result_item,omitempty"`
}

// Message is the protocol's WeixinMessage. User ids look like "xxx@im.wechat"
// and bot ids like "xxx@im.bot".
type Message struct {
	Seq          int64  `json:"seq,omitempty"`
	MessageID    int64  `json:"message_id,omitempty"`
	FromUserID   string `json:"from_user_id,omitempty"`
	ToUserID     string `json:"to_user_id,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	CreateTimeMS int64  `json:"create_time_ms,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	GroupID      string `json:"group_id,omitempty"`
	MessageType  int    `json:"message_type,omitempty"`
	MessageState int    `json:"message_state,omitempty"`

	ItemList []MessageItem `json:"item_list,omitempty"`

	// ContextToken must be echoed back on the reply. Without it the gateway
	// accepts the send but the message never reaches the user.
	ContextToken string `json:"context_token,omitempty"`
	RunID        string `json:"run_id,omitempty"`
}

// FromUser reports whether a message was authored by a human rather than
// echoed back from this bot. The long-poll stream contains both.
func (m *Message) FromUser() bool { return m != nil && m.MessageType == MessageTypeUser }

// PlainText is everything in the message a text-only agent can consume: text
// items plus the gateway's transcription of any voice item, joined by
// newlines. Media items contribute nothing and are silently skipped, so an
// image-only message yields "".
func (m *Message) PlainText() string {
	if m == nil {
		return ""
	}
	parts := make([]string, 0, len(m.ItemList))
	for i := range m.ItemList {
		item := &m.ItemList[i]
		switch {
		case item.Type == ItemTypeText && item.TextItem != nil && item.TextItem.Text != "":
			parts = append(parts, item.TextItem.Text)
		case item.Type == ItemTypeVoice && item.VoiceItem != nil && item.VoiceItem.Text != "":
			parts = append(parts, item.VoiceItem.Text)
		}
	}
	return joinNonEmpty(parts, "\n")
}

// PeerID is the conversation counterpart: the group for a group message,
// otherwise the sending user.
func (m *Message) PeerID() string {
	if m == nil {
		return ""
	}
	if m.GroupID != "" {
		return m.GroupID
	}
	return m.FromUserID
}

func joinNonEmpty(parts []string, sep string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += p
	}
	return out
}

// --- request / response envelopes ---

type GetUpdatesRequest struct {
	GetUpdatesBuf string    `json:"get_updates_buf"`
	BaseInfo      *BaseInfo `json:"base_info,omitempty"`
}

type GetUpdatesResponse struct {
	Ret     int        `json:"ret"`
	ErrCode int        `json:"errcode"`
	ErrMsg  string     `json:"errmsg"`
	Msgs    []*Message `json:"msgs"`
	// GetUpdatesBuf is the cursor to persist and replay on the next call.
	// An empty value means "keep the one you have", not "reset".
	GetUpdatesBuf       string `json:"get_updates_buf"`
	LongPollTimeoutMS   int    `json:"longpolling_timeout_ms"`
	SuggestedRetryAfter int    `json:"retry_after_ms"`
}

// Failed reports a gateway-level rejection. ret and errcode are both used
// depending on the endpoint, and either can carry the failure.
func (r *GetUpdatesResponse) Failed() bool {
	return r != nil && (r.Ret != 0 || r.ErrCode != 0)
}

// SessionExpired reports the unrecoverable "re-pair this account" condition.
func (r *GetUpdatesResponse) SessionExpired() bool {
	return r != nil && (r.ErrCode == ErrCodeSessionExpired || r.Ret == ErrCodeSessionExpired)
}

type SendMessageRequest struct {
	Msg      *Message  `json:"msg,omitempty"`
	BaseInfo *BaseInfo `json:"base_info,omitempty"`
}

type SendMessageResponse struct {
	Ret    int    `json:"ret"`
	ErrMsg string `json:"errmsg"`
}

type GetConfigRequest struct {
	ILinkUserID  string    `json:"ilink_user_id,omitempty"`
	ContextToken string    `json:"context_token,omitempty"`
	BaseInfo     *BaseInfo `json:"base_info,omitempty"`
}

type GetConfigResponse struct {
	Ret    int    `json:"ret"`
	ErrMsg string `json:"errmsg"`
	// TypingTicket is required by SendTyping and is scoped to one user.
	TypingTicket string `json:"typing_ticket"`
}

type SendTypingRequest struct {
	ILinkUserID  string    `json:"ilink_user_id,omitempty"`
	TypingTicket string    `json:"typing_ticket,omitempty"`
	Status       int       `json:"status,omitempty"`
	BaseInfo     *BaseInfo `json:"base_info,omitempty"`
}

type SendTypingResponse struct {
	Ret    int    `json:"ret"`
	ErrMsg string `json:"errmsg"`
}

type NotifyRequest struct {
	BaseInfo *BaseInfo `json:"base_info,omitempty"`
}

type NotifyResponse struct {
	Ret    int    `json:"ret"`
	ErrMsg string `json:"errmsg"`
}
