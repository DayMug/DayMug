package wechat

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Endpoint paths, relative to the account's base URL.
const (
	epGetUpdates  = "ilink/bot/getupdates"
	epSendMessage = "ilink/bot/sendmessage"
	epGetConfig   = "ilink/bot/getconfig"
	epSendTyping  = "ilink/bot/sendtyping"
	epNotifyStart = "ilink/bot/msg/notifystart"
	epNotifyStop  = "ilink/bot/msg/notifystop"
)

// GetUpdates holds a long poll open until the gateway has messages or its own
// timeout elapses. cursor is the previously persisted get_updates_buf; pass ""
// on the very first call.
//
// A client-side deadline expiry is reported as an error here rather than being
// swallowed — Poller decides whether an empty poll is normal, because only it
// knows whether the context was cancelled deliberately.
func (c *Client) GetUpdates(ctx context.Context, cursor string, timeout time.Duration) (*GetUpdatesResponse, error) {
	if timeout <= 0 {
		timeout = DefaultLongPollTimeout
	}
	req := &GetUpdatesRequest{GetUpdatesBuf: cursor, BaseInfo: c.baseInfo()}
	out := &GetUpdatesResponse{}
	if err := c.doJSON(ctx, epGetUpdates, req, out, timeout); err != nil {
		return nil, err
	}
	return out, nil
}

// SendTextOptions carries the addressing a text reply needs.
type SendTextOptions struct {
	// To is the recipient id, taken verbatim from the inbound message's
	// from_user_id (or group_id for a group).
	To string
	// ContextToken must come from the inbound message being replied to.
	// Sending without it is accepted by the gateway but silently undelivered,
	// so an empty value is rejected here instead.
	ContextToken string
	// RunID groups every message belonging to one agent turn. The Weixin
	// client folds items sharing a run id into a single progress block.
	RunID string
}

// ErrMissingContextToken guards the protocol's most costly silent failure.
var ErrMissingContextToken = errors.New("wechat: context_token is required to deliver a reply")

// SendText delivers a plain text message and returns the generated client id.
func (c *Client) SendText(ctx context.Context, text string, opts SendTextOptions) (string, error) {
	item := MessageItem{Type: ItemTypeText, TextItem: &TextItem{Text: text}}
	return c.SendItem(ctx, item, opts)
}

// SendItem delivers exactly one message item. Outbound messages are always
// StateFinish: the protocol has no edit-in-place, so a caller wanting
// streaming progress appends further items instead of revising this one.
func (c *Client) SendItem(ctx context.Context, item MessageItem, opts SendTextOptions) (string, error) {
	if opts.To == "" {
		return "", errors.New("wechat: recipient is required")
	}
	if opts.ContextToken == "" {
		return "", ErrMissingContextToken
	}
	clientID := newClientID()
	req := &SendMessageRequest{
		Msg: &Message{
			ToUserID:     opts.To,
			ClientID:     clientID,
			MessageType:  MessageTypeBot,
			MessageState: StateFinish,
			ItemList:     []MessageItem{item},
			ContextToken: opts.ContextToken,
			RunID:        opts.RunID,
		},
		BaseInfo: c.baseInfo(),
	}
	out := &SendMessageResponse{}
	if err := c.doJSON(ctx, epSendMessage, req, out, DefaultAPITimeout); err != nil {
		return "", err
	}
	if out.Ret != 0 {
		return "", &APIError{Endpoint: epSendMessage, Ret: out.Ret, Message: out.ErrMsg}
	}
	return clientID, nil
}

// GetConfig fetches per-user bot config, notably the ticket SendTyping needs.
func (c *Client) GetConfig(ctx context.Context, userID, contextToken string) (*GetConfigResponse, error) {
	req := &GetConfigRequest{ILinkUserID: userID, ContextToken: contextToken, BaseInfo: c.baseInfo()}
	out := &GetConfigResponse{}
	if err := c.doJSON(ctx, epGetConfig, req, out, DefaultLightTimeout); err != nil {
		return nil, err
	}
	if out.Ret != 0 {
		return nil, &APIError{Endpoint: epGetConfig, Ret: out.Ret, Message: out.ErrMsg}
	}
	return out, nil
}

// SendTyping sets or clears the typing indicator. status is TypingActive or
// TypingCancel.
func (c *Client) SendTyping(ctx context.Context, userID, ticket string, status int) error {
	req := &SendTypingRequest{
		ILinkUserID:  userID,
		TypingTicket: ticket,
		Status:       status,
		BaseInfo:     c.baseInfo(),
	}
	out := &SendTypingResponse{}
	if err := c.doJSON(ctx, epSendTyping, req, out, DefaultLightTimeout); err != nil {
		return err
	}
	if out.Ret != 0 {
		return &APIError{Endpoint: epSendTyping, Ret: out.Ret, Message: out.ErrMsg}
	}
	return nil
}

// NotifyStart tells the gateway this client is coming online.
func (c *Client) NotifyStart(ctx context.Context) error { return c.notify(ctx, epNotifyStart) }

// NotifyStop tells the gateway this client is going away. Callers should pass
// a context detached from the shutdown that triggered it, otherwise the
// notification is cancelled before it is sent.
func (c *Client) NotifyStop(ctx context.Context) error { return c.notify(ctx, epNotifyStop) }

func (c *Client) notify(ctx context.Context, endpoint string) error {
	out := &NotifyResponse{}
	if err := c.doJSON(ctx, endpoint, &NotifyRequest{BaseInfo: c.baseInfo()}, out, DefaultLightTimeout); err != nil {
		return err
	}
	if out.Ret != 0 {
		return &APIError{Endpoint: endpoint, Ret: out.Ret, Message: out.ErrMsg}
	}
	return nil
}

// TypingTickets caches the per-user typing ticket so the indicator costs one
// request instead of two after the first use. Tickets are scoped to a user and
// are refetched on demand; a failure is never fatal to a reply, so callers are
// expected to ignore the error.
type TypingTickets struct {
	client *Client
	mu     sync.Mutex
	byUser map[string]string
}

func NewTypingTickets(c *Client) *TypingTickets {
	return &TypingTickets{client: c, byUser: map[string]string{}}
}

// Set marks a user as typing (or clears it), fetching the ticket if needed.
func (t *TypingTickets) Set(ctx context.Context, userID, contextToken string, status int) error {
	ticket, err := t.ticket(ctx, userID, contextToken)
	if err != nil {
		return err
	}
	if err := t.client.SendTyping(ctx, userID, ticket, status); err != nil {
		// A rejected ticket is the expected way one expires; drop it so the
		// next call refetches instead of failing forever.
		t.forget(userID)
		return err
	}
	return nil
}

func (t *TypingTickets) ticket(ctx context.Context, userID, contextToken string) (string, error) {
	t.mu.Lock()
	cached, ok := t.byUser[userID]
	t.mu.Unlock()
	if ok && cached != "" {
		return cached, nil
	}
	cfg, err := t.client.GetConfig(ctx, userID, contextToken)
	if err != nil {
		return "", err
	}
	t.mu.Lock()
	t.byUser[userID] = cfg.TypingTicket
	t.mu.Unlock()
	return cfg.TypingTicket, nil
}

func (t *TypingTickets) forget(userID string) {
	t.mu.Lock()
	delete(t.byUser, userID)
	t.mu.Unlock()
}
