package imbot

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/imbot/wechat"
)

// wechatStub is a scriptable iLink gateway. Inbound messages are handed out one
// getUpdates call at a time; everything the connector sends is recorded.
type wechatStub struct {
	mu sync.Mutex

	inbound []map[string]any
	polls   int
	sent    []wechat.Message
	typing  []int
	// uploaded holds the ciphertext bodies the connector pushed to the CDN.
	uploaded [][]byte
	// media is served back for download requests, already encrypted.
	media []byte

	server *httptest.Server
}

func newWechatStub(t *testing.T, inbound ...map[string]any) *wechatStub {
	t.Helper()
	stub := &wechatStub{inbound: inbound}
	stub.server = httptest.NewServer(http.HandlerFunc(stub.handle))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *wechatStub) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case strings.HasSuffix(r.URL.Path, "getupdates"):
		idx := s.polls
		s.polls++
		if idx < len(s.inbound) {
			writeStubJSON(w, map[string]any{"ret": 0, "msgs": []map[string]any{s.inbound[idx]}})
			return
		}
		// Park briefly so the loop does not spin while a test asserts.
		time.Sleep(20 * time.Millisecond)
		writeStubJSON(w, map[string]any{"ret": 0})
	case strings.HasSuffix(r.URL.Path, "sendmessage"):
		var req wechat.SendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.Msg != nil {
			s.sent = append(s.sent, *req.Msg)
		}
		writeStubJSON(w, map[string]any{"ret": 0})
	case strings.HasSuffix(r.URL.Path, "getconfig"):
		writeStubJSON(w, map[string]any{"ret": 0, "typing_ticket": "tkt"})
	case strings.HasSuffix(r.URL.Path, "sendtyping"):
		var req wechat.SendTypingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			s.typing = append(s.typing, req.Status)
		}
		writeStubJSON(w, map[string]any{"ret": 0})
	case strings.HasSuffix(r.URL.Path, "getuploadurl"):
		writeStubJSON(w, map[string]any{"ret": 0, "upload_full_url": "http://" + r.Host + "/cdn/upload"})
	case strings.HasSuffix(r.URL.Path, "/cdn/upload"):
		body, _ := io.ReadAll(r.Body)
		s.uploaded = append(s.uploaded, body)
		w.Header().Set("x-encrypted-param", "dl-param")
		w.WriteHeader(http.StatusOK)
	case strings.HasSuffix(r.URL.Path, "/download"):
		_, _ = w.Write(s.media)
	default: // notifystart / notifystop
		writeStubJSON(w, map[string]any{"ret": 0})
	}
}

func writeStubJSON(w http.ResponseWriter, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *wechatStub) sentTexts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, msg := range s.sent {
		for _, item := range msg.ItemList {
			if item.TextItem != nil {
				out = append(out, item.TextItem.Text)
			}
		}
	}
	return out
}

func (s *wechatStub) sentMessages() []wechat.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

func (s *wechatStub) typingStatuses() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.typing)
}

func wechatInbound(from, text, contextToken string) map[string]any {
	return map[string]any{
		"from_user_id":  from,
		"to_user_id":    "bot@im.bot",
		"message_type":  wechat.MessageTypeUser,
		"context_token": contextToken,
		"item_list":     []map[string]any{{"type": wechat.ItemTypeText, "text_item": map[string]any{"text": text}}},
	}
}

// newTestWeChatConnector wires a connector to a stub gateway, bypassing the QR
// pairing that would otherwise be required.
func newTestWeChatConnector(stub *wechatStub) *WeChatConnector {
	c := NewWeChatConnector(WeChatConfig{Enabled: true, BotToken: "tok", BaseURL: stub.server.URL})
	c.client = wechat.NewClient(wechat.ClientOptions{
		BaseURL: stub.server.URL, CDNBase: stub.server.URL, Token: "tok",
	})
	c.typing = wechat.NewTypingTickets(c.client)
	return c
}

// collectMessages runs the connector until it has delivered want messages.
func collectMessages(t *testing.T, c *WeChatConnector, want int) []Message {
	t.Helper()
	var (
		mu   sync.Mutex
		got  []Message
		done = make(chan struct{})
	)
	c.SetOnMessage(func(msg Message) {
		mu.Lock()
		got = append(got, msg)
		if len(got) == want {
			close(done)
		}
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer c.Stop()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("timed out after %d/%d messages: %+v", len(got), want, got)
	}
	mu.Lock()
	defer mu.Unlock()
	return slices.Clone(got)
}

func TestWeChatNormalizesDirectMessage(t *testing.T) {
	stub := newWechatStub(t, wechatInbound("u1@im.wechat", "你好", "ctx-1"))
	c := newTestWeChatConnector(stub)

	got := collectMessages(t, c, 1)[0]

	if got.Platform != PlatformWeChat {
		t.Errorf("Platform = %q", got.Platform)
	}
	if got.ChannelID != "u1@im.wechat" {
		t.Errorf("ChannelID = %q", got.ChannelID)
	}
	if got.SenderID != "u1@im.wechat" {
		t.Errorf("SenderID = %q", got.SenderID)
	}
	if got.Text != "你好" {
		t.Errorf("Text = %q", got.Text)
	}
	if !got.IsDM {
		t.Error("a private chat must be marked as a DM")
	}
	// Every private message addresses the bot; there is no @ to look for.
	if !got.Mentioned {
		t.Error("a DM must count as addressing the bot")
	}
	if got.FromBot {
		t.Error("Weixin carries no bot identities; FromBot must stay false")
	}
	// The protocol exposes no history read, so promising backfill would lie.
	if got.LoadThreadMessages != nil {
		t.Error("WeChat cannot backfill thread history; LoadThreadMessages must stay nil")
	}
}

// The stream echoes the bot's own sends back; answering them would loop.
func TestWeChatIgnoresItsOwnEcho(t *testing.T) {
	echo := wechatInbound("bot@im.bot", "previous answer", "ctx-1")
	echo["message_type"] = wechat.MessageTypeBot
	stub := newWechatStub(t, echo, wechatInbound("u1@im.wechat", "real question", "ctx-2"))
	c := newTestWeChatConnector(stub)

	got := collectMessages(t, c, 1)
	if len(got) != 1 || got[0].Text != "real question" {
		t.Fatalf("delivered %+v, want only the user message", got)
	}
}

func TestWeChatSlashNewRotatesThread(t *testing.T) {
	stub := newWechatStub(t,
		wechatInbound("u1@im.wechat", "第一个话题", "ctx-1"),
		wechatInbound("u1@im.wechat", "/new 第二个话题", "ctx-2"),
		wechatInbound("u1@im.wechat", "接着第二个说", "ctx-3"),
	)
	c := newTestWeChatConnector(stub)

	got := collectMessages(t, c, 3)

	if got[0].ThreadID == got[1].ThreadID {
		t.Errorf("/new did not rotate the thread (%q)", got[0].ThreadID)
	}
	if got[1].Text != "第二个话题" {
		t.Errorf("Text = %q, want the /new payload without the command", got[1].Text)
	}
	if got[2].ThreadID != got[1].ThreadID {
		t.Errorf("follow-up thread = %q, want the rotated %q", got[2].ThreadID, got[1].ThreadID)
	}
}

// A bare /new produces no prompt, but it still has to reach the bridge: the
// bridge is what gates the reply on the channel rules and posts the
// confirmation. Dropping it in the connector is what made /new silent.
func TestWeChatBareSlashNewDeliversAnAckWithNoPrompt(t *testing.T) {
	stub := newWechatStub(t,
		wechatInbound("u1@im.wechat", "/new", "ctx-1"),
		wechatInbound("u1@im.wechat", "现在问", "ctx-2"),
	)
	c := newTestWeChatConnector(stub)

	got := collectMessages(t, c, 2)
	if len(got) != 2 {
		t.Fatalf("delivered %+v, want the /new and the follow-up", got)
	}
	if got[0].Text != "" {
		t.Errorf("bare /new carried prompt %q, want none", got[0].Text)
	}
	if got[0].Ack == "" {
		t.Error("bare /new carried no confirmation; the user cannot tell the reset landed")
	}
	if got[1].Text != "现在问" || got[1].Ack != "" {
		t.Errorf("follow-up = %+v, want the plain prompt", got[1])
	}
	if got[1].ThreadID != got[0].ThreadID {
		t.Errorf("follow-up thread = %q, want the thread /new opened %q", got[1].ThreadID, got[0].ThreadID)
	}
}

// "/new <text>" both resets and asks. The answer alone cannot tell the user
// whether the reset landed or whether "/new" was passed through as prose.
func TestWeChatSlashNewWithTextAcksAndStillAsks(t *testing.T) {
	stub := newWechatStub(t, wechatInbound("u1@im.wechat", "/new 帮我看看部署", "ctx-1"))
	c := newTestWeChatConnector(stub)

	got := collectMessages(t, c, 1)[0]
	if got.Text != "帮我看看部署" {
		t.Errorf("prompt = %q, want the text after /new", got.Text)
	}
	if got.Ack == "" {
		t.Error("/new with text carried no confirmation")
	}
}

func TestWeChatMessageIDIsScopedToThePeer(t *testing.T) {
	inbound := wechatInbound("u1@im.wechat", "hi", "ctx-1")
	inbound["message_id"] = 7
	stub := newWechatStub(t, inbound)
	c := newTestWeChatConnector(stub)

	got := collectMessages(t, c, 1)[0]
	// The gateway's message id is only unique per conversation, but the bridge
	// dedups globally.
	if got.MessageID != "u1@im.wechat|7" {
		t.Errorf("MessageID = %q, want the peer folded in", got.MessageID)
	}
}

// Weixin cannot edit a sent message, so intermediate progress must not be
// posted — the user would be left with a stale "thinking…" above the answer.
func TestWeChatResponderPostsOnlyTheFinalAnswer(t *testing.T) {
	stub := newWechatStub(t, wechatInbound("u1@im.wechat", "问题", "ctx-1"))
	c := newTestWeChatConnector(stub)
	msg := collectMessages(t, c, 1)[0]

	responder := c.Responder(msg)
	ctx := context.Background()
	if err := responder.Start(ctx, "💭 正在思考…"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := responder.Update(ctx, "💭 还在思考…"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := stub.sentTexts(); len(got) != 0 {
		t.Fatalf("progress leaked to the user: %q", got)
	}

	if err := responder.Complete(ctx, "最终答案"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got := stub.sentTexts()
	if len(got) != 1 || got[0] != "最终答案" {
		t.Fatalf("sent %q, want just the final answer", got)
	}
}

// The typing indicator is the only progress signal the platform offers.
func TestWeChatResponderDrivesTypingIndicator(t *testing.T) {
	stub := newWechatStub(t, wechatInbound("u1@im.wechat", "问题", "ctx-1"))
	c := newTestWeChatConnector(stub)
	msg := collectMessages(t, c, 1)[0]

	responder := c.Responder(msg)
	ctx := context.Background()
	_ = responder.Start(ctx, "…")
	_ = responder.Complete(ctx, "答案")

	got := stub.typingStatuses()
	if len(got) < 2 {
		t.Fatalf("typing statuses = %v, want it set then cleared", got)
	}
	if got[0] != wechat.TypingActive {
		t.Errorf("first typing status = %d, want active", got[0])
	}
	if got[len(got)-1] != wechat.TypingCancel {
		t.Errorf("last typing status = %d, want cancel", got[len(got)-1])
	}
}

// Staying silent is right for progress the answer will supersede, but a run
// that failed produces no answer: the notice is all the reader ever gets.
func TestWeChatResponderDeliversNotices(t *testing.T) {
	stub := newWechatStub(t, wechatInbound("u1@im.wechat", "问题", "ctx-1"))
	c := newTestWeChatConnector(stub)
	msg := collectMessages(t, c, 1)[0]

	responder := c.Responder(msg)
	ctx := context.Background()
	if err := Notify(ctx, responder, "⚠️ 运行失败: boom"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	got := stub.sentTexts()
	if len(got) != 1 || got[0] != "⚠️ 运行失败: boom" {
		t.Fatalf("sent %q, want the failure notice", got)
	}
}

// The bridge closes the responder on every exit path, so a turn that never
// reaches Complete must still take the indicator down.
func TestWeChatResponderClearsTypingWhenTurnFails(t *testing.T) {
	stub := newWechatStub(t, wechatInbound("u1@im.wechat", "问题", "ctx-1"))
	c := newTestWeChatConnector(stub)
	msg := collectMessages(t, c, 1)[0]

	responder := c.Responder(msg)
	ctx := context.Background()
	_ = responder.Start(ctx, "…")
	CloseResponder(ctx, responder)

	got := stub.typingStatuses()
	if len(got) == 0 || got[len(got)-1] != wechat.TypingCancel {
		t.Fatalf("typing statuses = %v, want the indicator cleared", got)
	}
}

func TestWeChatReplyCarriesContextTokenAndRunID(t *testing.T) {
	stub := newWechatStub(t, wechatInbound("u1@im.wechat", "问题", "ctx-abc"))
	c := newTestWeChatConnector(stub)
	msg := collectMessages(t, c, 1)[0]

	if err := c.Responder(msg).Complete(context.Background(), "答案"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	sent := stub.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sent))
	}
	// Without the inbound token the gateway accepts the send and never
	// delivers it.
	if sent[0].ContextToken != "ctx-abc" {
		t.Errorf("context_token = %q, want the inbound token", sent[0].ContextToken)
	}
	// Sharing a run id is what makes the client fold a turn into one block.
	if sent[0].RunID != msg.ThreadID {
		t.Errorf("run_id = %q, want the thread id %q", sent[0].RunID, msg.ThreadID)
	}
}

func TestWeChatStartRefusesUnpairedBot(t *testing.T) {
	c := NewWeChatConnector(WeChatConfig{Enabled: true})
	err := c.Start(context.Background())
	if err == nil {
		t.Fatal("Start succeeded without a pairing")
	}
	if !strings.Contains(err.Error(), "not paired") {
		t.Errorf("err = %v, want it to point at pairing", err)
	}
}

func TestWeChatStopIsIdempotent(t *testing.T) {
	stub := newWechatStub(t)
	c := newTestWeChatConnector(stub)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	c.Stop()
	c.Stop() // must not panic or block
}

func TestWeChatIsRegistered(t *testing.T) {
	caps, ok := CapabilitiesFor(PlatformWeChat)
	if !ok {
		t.Fatal("wechat must be present in the platform registry")
	}
	if caps.DisplayName != "微信" {
		t.Errorf("display name = %q", caps.DisplayName)
	}
	// The iLink stream carries no bot identities, so a relay has no target.
	if caps.SupportsBotRelay {
		t.Error("WeChat cannot relay between bots")
	}
	if !slices.Contains(SupportedPlatforms(), PlatformWeChat) {
		t.Error("SupportedPlatforms must list wechat")
	}
}

func TestValidateBotConfigWeChatRequiresPairing(t *testing.T) {
	_, err := ValidateBotConfig(BotConfig{ID: "b1", Platform: PlatformWeChat, Enabled: true, BotToken: "tok"})
	if err == nil {
		t.Fatal("a bot with a token but no gateway was accepted")
	}

	got, err := ValidateBotConfig(BotConfig{
		ID: "b1", Platform: PlatformWeChat, Enabled: true,
		BotToken: "tok", AppID: "https://acct.example", AppSecret: "leftover", AppToken: "leftover",
	})
	if err != nil {
		t.Fatalf("ValidateBotConfig: %v", err)
	}
	if got.AppSecret != "" || got.AppToken != "" {
		t.Errorf("unused credential slots not cleared: %+v", got)
	}
	if got.AppID != "https://acct.example" {
		t.Errorf("AppID = %q, want the paired gateway preserved", got.AppID)
	}
}

func TestWeChatInboundImageBecomesADownloadableAttachment(t *testing.T) {
	key := []byte("0123456789abcdef")
	plaintext := []byte("image bytes")
	stub := newWechatStub(t, map[string]any{
		"from_user_id":  "u1@im.wechat",
		"message_type":  wechat.MessageTypeUser,
		"context_token": "ctx-1",
		"item_list": []map[string]any{{
			"type":   wechat.ItemTypeImage,
			"aeskey": hex.EncodeToString(key),
			"image_item": map[string]any{
				"aeskey": hex.EncodeToString(key),
				"media":  map[string]any{"encrypt_query_param": "p1"},
			},
		}},
	})
	cipher, err := wechatTestEncrypt(plaintext, key)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	stub.mu.Lock()
	stub.media = cipher
	stub.mu.Unlock()

	c := newTestWeChatConnector(stub)
	// A media-only message has no text, but it must still reach the bridge.
	got := collectMessages(t, c, 1)[0]

	if len(got.Attachments) != 1 {
		t.Fatalf("attachments = %+v, want one", got.Attachments)
	}
	if got.Attachments[0].Name != "image.jpg" {
		t.Errorf("name = %q", got.Attachments[0].Name)
	}

	var buf bytes.Buffer
	if err := got.Attachments[0].Download(context.Background(), &buf); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if buf.String() != string(plaintext) {
		t.Errorf("downloaded %q, want %q", buf.String(), plaintext)
	}
}

func TestWeChatPostAttachmentsUploadsAndSendsMedia(t *testing.T) {
	stub := newWechatStub(t, wechatInbound("u1@im.wechat", "画个图", "ctx-1"))
	c := newTestWeChatConnector(stub)
	msg := collectMessages(t, c, 1)[0]

	responder, ok := c.Responder(msg).(ArtifactResponder)
	if !ok {
		t.Fatal("the WeChat responder must be able to post artifacts")
	}
	payload := []byte("PNG bytes")
	err := responder.PostAttachments(context.Background(), []OutboundAttachment{{
		Name: "chart.png", MIME: "image/png", Size: int64(len(payload)),
		Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil },
	}})
	if err != nil {
		t.Fatalf("PostAttachments: %v", err)
	}

	stub.mu.Lock()
	uploaded := len(stub.uploaded)
	stub.mu.Unlock()
	if uploaded != 1 {
		t.Fatalf("uploaded %d bodies, want 1", uploaded)
	}

	sent := stub.sentMessages()
	if len(sent) != 1 || len(sent[0].ItemList) != 1 {
		t.Fatalf("sent = %+v, want one message with one item", sent)
	}
	item := sent[0].ItemList[0]
	// An image MIME must arrive as an inline image, not a file attachment.
	if item.Type != wechat.ItemTypeImage || item.ImageItem == nil {
		t.Fatalf("item = %+v, want an image item", item)
	}
	if item.ImageItem.Media.EncryptQueryParam != "dl-param" {
		t.Errorf("encrypt_query_param = %q, want the CDN's header value",
			item.ImageItem.Media.EncryptQueryParam)
	}
	if sent[0].ContextToken != "ctx-1" {
		t.Errorf("context_token = %q, want the inbound token", sent[0].ContextToken)
	}
}

// Outbound media announces its key in media.aes_key as base64 of the key's hex
// *text* — the encoding Tencent's own plugin emits for every media type. Base64
// of the raw 16 bytes uploads fine and gets ret=0, but the client cannot open
// the blob and renders it blank, so nothing on our side ever errors.
func TestWeChatOutboundMediaKeyIsBase64OfHexText(t *testing.T) {
	tests := []struct {
		name       string
		attachment OutboundAttachment
		media      func(wechat.MessageItem) *wechat.CDNMedia
	}{
		{
			name:       "image",
			attachment: OutboundAttachment{Name: "chart.png", MIME: "image/png"},
			media:      func(i wechat.MessageItem) *wechat.CDNMedia { return i.ImageItem.Media },
		},
		{
			name:       "file",
			attachment: OutboundAttachment{Name: "report.pdf", MIME: "application/pdf"},
			media:      func(i wechat.MessageItem) *wechat.CDNMedia { return i.FileItem.Media },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newWechatStub(t, wechatInbound("u1@im.wechat", "发个附件", "ctx-1"))
			c := newTestWeChatConnector(stub)
			msg := collectMessages(t, c, 1)[0]

			payload := []byte("media bytes")
			attachment := tt.attachment
			attachment.Size = int64(len(payload))
			attachment.Open = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(payload)), nil
			}
			responder := c.Responder(msg).(ArtifactResponder)
			if err := responder.PostAttachments(context.Background(), []OutboundAttachment{attachment}); err != nil {
				t.Fatalf("PostAttachments: %v", err)
			}

			item := stub.sentMessages()[0].ItemList[0]
			// Decoded the long way round on purpose: the test re-derives the
			// documented wire format instead of trusting the encoder it checks.
			hexText, err := base64.StdEncoding.DecodeString(tt.media(item).AESKey)
			if err != nil {
				t.Fatalf("aes_key is not base64: %v", err)
			}
			key, err := hex.DecodeString(string(hexText))
			if err != nil {
				t.Fatalf("aes_key %q does not base64-decode to hex text: %v", tt.media(item).AESKey, err)
			}

			// The real proof is that this key opens the bytes actually pushed to
			// the CDN, not merely that the field parses.
			stub.mu.Lock()
			ciphertext := slices.Clone(stub.uploaded[0])
			stub.mu.Unlock()
			want, err := wechatTestEncrypt(payload, key)
			if err != nil {
				t.Fatalf("encrypt oracle: %v", err)
			}
			if !bytes.Equal(ciphertext, want) {
				t.Error("the announced aes_key does not match the uploaded ciphertext")
			}
		})
	}
}

// The gateway sets an item-level `aeskey` on inbound images; Tencent's plugin
// never sends one outbound. Shipping an extra field the reference omits is how
// the previous blank-image fix attempt went wrong.
func TestWeChatOutboundImageOmitsTheItemLevelKey(t *testing.T) {
	stub := newWechatStub(t, wechatInbound("u1@im.wechat", "画个图", "ctx-1"))
	c := newTestWeChatConnector(stub)
	msg := collectMessages(t, c, 1)[0]

	payload := []byte("PNG bytes")
	responder := c.Responder(msg).(ArtifactResponder)
	err := responder.PostAttachments(context.Background(), []OutboundAttachment{{
		Name: "chart.png", MIME: "image/png", Size: int64(len(payload)),
		Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil },
	}})
	if err != nil {
		t.Fatalf("PostAttachments: %v", err)
	}

	if got := stub.sentMessages()[0].ItemList[0].ImageItem.AESKey; got != "" {
		t.Errorf("image item aeskey = %q, want it absent from an outbound image", got)
	}
}

func TestWeChatNonImageArtifactIsSentAsAFile(t *testing.T) {
	stub := newWechatStub(t, wechatInbound("u1@im.wechat", "报告", "ctx-1"))
	c := newTestWeChatConnector(stub)
	msg := collectMessages(t, c, 1)[0]

	responder := c.Responder(msg).(ArtifactResponder)
	payload := []byte("%PDF-1.4")
	err := responder.PostAttachments(context.Background(), []OutboundAttachment{{
		Name: "report.pdf", MIME: "application/pdf", Size: int64(len(payload)),
		Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(payload)), nil },
	}})
	if err != nil {
		t.Fatalf("PostAttachments: %v", err)
	}

	item := stub.sentMessages()[0].ItemList[0]
	if item.Type != wechat.ItemTypeFile || item.FileItem == nil {
		t.Fatalf("item = %+v, want a file item", item)
	}
	if item.FileItem.FileName != "report.pdf" {
		t.Errorf("file_name = %q", item.FileItem.FileName)
	}
	// len describes the plaintext, which is what the recipient sees.
	if item.FileItem.Len != strconv.Itoa(len(payload)) {
		t.Errorf("len = %q, want the plaintext size %d", item.FileItem.Len, len(payload))
	}
}

// wechatTestEncrypt is an independent AES-128-ECB + PKCS#7 oracle. Writing it
// out here rather than reusing the connector's own crypto keeps this test
// honest: a bug in the implementation cannot cancel itself out.
func wechatTestEncrypt(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	size := block.BlockSize()
	pad := size - len(plaintext)%size
	padded := append(slices.Clone(plaintext), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += size {
		block.Encrypt(out[i:i+size], padded[i:i+size])
	}
	return out, nil
}
