package imbot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type feishuMockHTTP struct {
	do func(*http.Request) (*http.Response, error)
}

func (m *feishuMockHTTP) Do(req *http.Request) (*http.Response, error) {
	return m.do(req)
}

func feishuJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func sp(s string) *string { return &s }

func feishuTextEvent(mutate func(*larkim.P2MessageReceiveV1)) *larkim.P2MessageReceiveV1 {
	event := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender: &larkim.EventSender{
			SenderType: sp("user"),
			SenderId:   &larkim.UserId{OpenId: sp("ou_alice")},
		},
		Message: &larkim.EventMessage{
			MessageId:   sp("om_1"),
			ChatId:      sp("oc_1"),
			ChatType:    sp("group"),
			MessageType: sp("text"),
			Content:     sp(`{"text":"hello"}`),
		},
	}}
	if mutate != nil {
		mutate(event)
	}
	return event
}

func collectFeishuMessages(f *FeishuConnector) *[]Message {
	var got []Message
	f.SetOnMessage(func(m Message) { got = append(got, m) })
	return &got
}

func TestFeishuHandleReceiveTextWithMentions(t *testing.T) {
	f := NewFeishuConnector(FeishuConfig{})
	f.botOpenID = "ou_bot"
	got := collectFeishuMessages(f)

	f.handleReceive(context.Background(), feishuTextEvent(func(e *larkim.P2MessageReceiveV1) {
		e.Event.Message.Content = sp(`{"text":"@_user_1 deploy for @_user_2 now"}`)
		e.Event.Message.Mentions = []*larkim.MentionEvent{
			{Key: sp("@_user_1"), Id: &larkim.UserId{OpenId: sp("ou_bot")}},
			{Key: sp("@_user_2"), Id: &larkim.UserId{OpenId: sp("ou_carol")}, Name: sp("Carol")},
		}
	}))

	if len(*got) != 1 {
		t.Fatalf("got %d messages, want 1", len(*got))
	}
	msg := (*got)[0]
	if !msg.Mentioned {
		t.Fatal("bot mention not detected")
	}
	if msg.Text != "deploy for @Carol now" {
		t.Fatalf("text = %q; bot mention must be stripped, other mentions kept as @name", msg.Text)
	}
	if msg.ChannelID != "oc_1" || msg.SenderID != "ou_alice" || msg.IsDM {
		t.Fatalf("unexpected message: %+v", msg)
	}
}

func TestFeishuHandleReceiveRecordsSenderAsMentionable(t *testing.T) {
	mockHTTP := &feishuMockHTTP{do: func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return feishuJSONResponse(`{"code":0,"msg":"success","tenant_access_token":"token","expire":7200}`), nil
		case "/open-apis/contact/v3/users/ou_alice":
			return feishuJSONResponse(`{"code":0,"msg":"success","data":{"user":{"name":"Alice"}}}`), nil
		default:
			t.Fatalf("unexpected Feishu request: %s %s", req.Method, req.URL.String())
			return nil, nil
		}
	}}
	f := NewFeishuConnector(FeishuConfig{AppID: "app", AppSecret: "secret"})
	f.rest = lark.NewClient("app", "secret", lark.WithHttpClient(mockHTTP))
	f.botOpenID = "ou_bot"
	got := collectFeishuMessages(f)

	f.handleReceive(context.Background(), feishuTextEvent(func(e *larkim.P2MessageReceiveV1) {
		e.Event.Message.ThreadId = sp("omt_thread")
	}))
	if len(*got) != 1 {
		t.Fatalf("got %d messages, want 1", len(*got))
	}
	participants := f.mentions.participants(mentionThreadKey("oc_1", "omt_thread"))
	if participants["Alice"] != "ou_alice" {
		t.Fatalf("participants = %v, want the sender mentionable by name", participants)
	}
}

// The live inbound path and the history backfill inline mentions through the
// same helper; this pins the two to the identical result for the identical
// message, including the open_id fallback for a mention that carries no name.
func TestFeishuHistoryMessageInlinesMentionsLikeLivePath(t *testing.T) {
	const content = `{"text":"@_user_1 deploy for @_user_2 with @_user_3 now"}`
	const want = "deploy for @Carol with @ou_dave now"

	f := NewFeishuConnector(FeishuConfig{})
	f.botOpenID = "ou_bot"
	got := collectFeishuMessages(f)
	f.handleReceive(context.Background(), feishuTextEvent(func(e *larkim.P2MessageReceiveV1) {
		e.Event.Message.Content = sp(content)
		e.Event.Message.Mentions = []*larkim.MentionEvent{
			{Key: sp("@_user_1"), Id: &larkim.UserId{OpenId: sp("ou_bot")}},
			{Key: sp("@_user_2"), Id: &larkim.UserId{OpenId: sp("ou_carol")}, Name: sp("Carol")},
			{Key: sp("@_user_3"), Id: &larkim.UserId{OpenId: sp("ou_dave")}},
		}
	}))
	if len(*got) != 1 || (*got)[0].Text != want || !(*got)[0].Mentioned {
		t.Fatalf("live path = %+v, want text %q and a bot mention", *got, want)
	}

	history, ok := f.historyMessage(context.Background(), &larkim.Message{
		MessageId: sp("om_1"),
		MsgType:   sp("text"),
		Body:      &larkim.MessageBody{Content: sp(content)},
		Sender:    &larkim.Sender{Id: sp("ou_alice"), SenderType: sp("user")},
		Mentions: []*larkim.Mention{
			{Key: sp("@_user_1"), Id: sp("ou_bot")},
			{Key: sp("@_user_2"), Id: sp("ou_carol"), Name: sp("Carol")},
			{Key: sp("@_user_3"), Id: sp("ou_dave")},
		},
	}, "oc_1", "om_root", false)
	if !ok || history.Text != want || !history.Mentioned {
		t.Fatalf("history path = %+v (ok=%v), want text %q and a bot mention", history, ok, want)
	}
}

func TestFeishuHandleReceiveThreadDerivation(t *testing.T) {
	tests := []struct {
		name       string
		threadID   *string
		rootID     *string
		wantThread string
	}{
		{"topic group thread_id wins", sp("omt_topic"), sp("om_root"), "omt_topic"},
		{"reply chain root_id", nil, sp("om_root"), "om_root"},
		{"top-level message roots its own thread", nil, nil, "om_1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFeishuConnector(FeishuConfig{})
			got := collectFeishuMessages(f)
			f.handleReceive(context.Background(), feishuTextEvent(func(e *larkim.P2MessageReceiveV1) {
				e.Event.Message.ThreadId = tt.threadID
				e.Event.Message.RootId = tt.rootID
			}))
			if len(*got) != 1 || (*got)[0].ThreadID != tt.wantThread {
				t.Fatalf("messages = %+v, want thread %q", *got, tt.wantThread)
			}
		})
	}
}

func TestFeishuHandleReceiveAttachesThreadLoader(t *testing.T) {
	f := NewFeishuConnector(FeishuConfig{})
	f.rest = lark.NewClient("app", "secret")
	got := collectFeishuMessages(f)
	f.handleReceive(context.Background(), feishuTextEvent(func(e *larkim.P2MessageReceiveV1) {
		e.Event.Message.RootId = sp("om_root")
		e.Event.Message.CreateTime = sp("3000")
	}))
	if len(*got) != 1 || (*got)[0].LoadThreadMessages == nil {
		t.Fatalf("reply did not carry a Feishu history loader: %+v", *got)
	}
}

func TestFeishuThreadLoaderLoadsWebhookRootAndReplies(t *testing.T) {
	var listQueries []url.Values
	mockHTTP := &feishuMockHTTP{do: func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return feishuJSONResponse(`{"code":0,"msg":"success","tenant_access_token":"token","expire":7200}`), nil
		case "/open-apis/im/v1/messages/om_root":
			return feishuJSONResponse(`{
				"code":0,"msg":"success","data":{"items":[{
					"message_id":"om_root","thread_id":"omt_topic","msg_type":"interactive","create_time":"1000",
					"chat_id":"oc_1","sender":{"id":"cli_alert","sender_type":"app","sender_name":"PDF Export"},
					"body":{"content":"{\"header\":{\"title\":{\"tag\":\"plain_text\",\"content\":\"PDF export failed\"}},\"body\":{\"elements\":[{\"tag\":\"markdown\",\"content\":\"Bad Gateway\"}]}}"}
				}]}}
			`), nil
		case "/open-apis/im/v1/messages":
			listQueries = append(listQueries, req.URL.Query())
			// Pages come back newest-first, matching the ByCreateTimeDesc the
			// loader asks for.
			if req.URL.Query().Get("page_token") == "next" {
				return feishuJSONResponse(`{
					"code":0,"msg":"success","data":{"has_more":false,"items":[
						{"message_id":"om_root","thread_id":"omt_topic","msg_type":"interactive","create_time":"1000","sender":{"id":"cli_alert","sender_type":"app","sender_name":"PDF Export"},"body":{"content":"{\"header\":{\"title\":{\"tag\":\"plain_text\",\"content\":\"PDF export failed\"}},\"body\":{\"elements\":[{\"tag\":\"markdown\",\"content\":\"Bad Gateway\"}]}}"}}
					]}}
				`), nil
			}
			return feishuJSONResponse(`{
				"code":0,"msg":"success","data":{"has_more":true,"page_token":"next","items":[
					{"message_id":"om_current","root_id":"om_root","thread_id":"omt_topic","msg_type":"text","create_time":"4000","sender":{"id":"ou_current","sender_type":"user","sender_name":"Current"},"body":{"content":"{\"text\":\"check it\"}"}},
					{"message_id":"om_self","root_id":"om_root","thread_id":"omt_topic","msg_type":"text","create_time":"3000","sender":{"id":"daymug_app","sender_type":"app","sender_name":"DayMug"},"body":{"content":"{\"text\":\"processing\"}"}},
					{"message_id":"om_other","root_id":"om_other","thread_id":"omt_other","msg_type":"text","create_time":"2500","sender":{"id":"ou_other","sender_type":"user","sender_name":"Other"},"body":{"content":"{\"text\":\"unrelated\"}"}},
					{"message_id":"om_reply","root_id":"om_root","thread_id":"omt_topic","msg_type":"text","create_time":"2000","sender":{"id":"ou_alice","sender_type":"user","sender_name":"Alice"},"body":{"content":"{\"text\":\"task 3846276\"}"}}
				]}}
			`), nil
		default:
			t.Fatalf("unexpected Feishu request: %s", req.URL.String())
			return nil, nil
		}
	}}
	f := NewFeishuConnector(FeishuConfig{AppID: "daymug_app"})
	f.rest = lark.NewClient("daymug_app", "secret", lark.WithHttpClient(mockHTTP))

	loader := f.threadMessageLoader("oc_1", "omt_topic", "om_root", "om_current", "4000", false)
	history, err := loader(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history = %+v, want webhook root and one human reply", history)
	}
	if history[0].MessageID != "om_root" || history[0].SenderName != "PDF Export" || !history[0].FromBot || history[0].Text != "PDF export failed\nBad Gateway" {
		t.Fatalf("webhook root not normalized: %+v", history[0])
	}
	if history[1].MessageID != "om_reply" || history[1].Text != "task 3846276" || history[1].FromBot {
		t.Fatalf("human reply not normalized: %+v", history[1])
	}
	if len(listQueries) != 2 || listQueries[0].Get("container_id") != "oc_1" || listQueries[0].Get("sort_type") != "ByCreateTimeDesc" || listQueries[0].Get("start_time") != "1" || listQueries[0].Get("end_time") != "4" {
		t.Fatalf("unexpected history queries: %+v", listQueries)
	}
}

func TestFeishuThreadHistoryCursorSkipsAlreadyImportedMessages(t *testing.T) {
	f := NewFeishuConnector(FeishuConfig{})
	raw := []*larkim.Message{
		{MessageId: sp("om_root"), ThreadId: sp("omt_topic"), MsgType: sp("text"), CreateTime: sp("1000"), Body: &larkim.MessageBody{Content: sp(`{"text":"root"}`)}},
		{MessageId: sp("om_seen"), RootId: sp("om_root"), ThreadId: sp("omt_topic"), MsgType: sp("text"), CreateTime: sp("2000"), Body: &larkim.MessageBody{Content: sp(`{"text":"seen"}`)}},
		{MessageId: sp("om_new"), RootId: sp("om_root"), ThreadId: sp("omt_topic"), MsgType: sp("text"), CreateTime: sp("3000"), Body: &larkim.MessageBody{Content: sp(`{"text":"new"}`)}},
	}
	history := f.normalizeThreadMessages(context.Background(), raw, "oc_1", "omt_topic", "om_root", "om_current", "4000", "om_seen", false)
	if len(history) != 1 || history[0].MessageID != "om_new" {
		t.Fatalf("history after cursor = %+v, want only om_new", history)
	}
}

func TestFeishuHandleReceiveSkips(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*larkim.P2MessageReceiveV1)
	}{
		{"app sender", func(e *larkim.P2MessageReceiveV1) { e.Event.Sender.SenderType = sp("app") }},
		{"own echo", func(e *larkim.P2MessageReceiveV1) { e.Event.Sender.SenderId.OpenId = sp("ou_bot") }},
		{"unsupported message type", func(e *larkim.P2MessageReceiveV1) { e.Event.Message.MessageType = sp("file") }},
		{"empty text", func(e *larkim.P2MessageReceiveV1) { e.Event.Message.Content = sp(`{"text":"  "}`) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFeishuConnector(FeishuConfig{})
			f.botOpenID = "ou_bot"
			got := collectFeishuMessages(f)
			f.handleReceive(context.Background(), feishuTextEvent(tt.mutate))
			if len(*got) != 0 {
				t.Fatalf("expected drop, got %+v", *got)
			}
		})
	}
}

func TestFeishuHandleReceiveDM(t *testing.T) {
	f := NewFeishuConnector(FeishuConfig{})
	got := collectFeishuMessages(f)
	f.handleReceive(context.Background(), feishuTextEvent(func(e *larkim.P2MessageReceiveV1) {
		e.Event.Message.ChatType = sp("p2p")
	}))
	if len(*got) != 1 || !(*got)[0].IsDM {
		t.Fatalf("DM not detected: %+v", *got)
	}
}

func TestFeishuMentionTagUsesNativeTextMarkup(t *testing.T) {
	f := NewFeishuConnector(FeishuConfig{BotName: "Guard & Review"})
	f.botOpenID = "ou_guard"

	if got, want := f.MentionTag(), `<at user_id="ou_guard">Guard &amp; Review</at>`; got != want {
		t.Fatalf("MentionTag() = %q, want %q", got, want)
	}
}

func TestFeishuResponderPostsHandoffAsThreadText(t *testing.T) {
	var requestBody string
	mockHTTP := &feishuMockHTTP{do: func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return feishuJSONResponse(`{"code":0,"msg":"success","tenant_access_token":"token","expire":7200}`), nil
		case "/open-apis/im/v1/messages/om_root/reply":
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read handoff request: %v", err)
			}
			requestBody = string(body)
			return feishuJSONResponse(`{"code":0,"msg":"success","data":{"message_id":"om_handoff"}}`), nil
		default:
			t.Fatalf("unexpected Feishu request: %s", req.URL.String())
			return nil, nil
		}
	}}
	f := NewFeishuConnector(FeishuConfig{AppID: "app", AppSecret: "secret"})
	f.rest = lark.NewClient("app", "secret", lark.WithHttpClient(mockHTTP))
	responder := f.Responder(Message{MessageID: "om_root"})
	poster, ok := responder.(interface {
		PostHandoff(context.Context, string) (string, error)
	})
	if !ok {
		t.Fatal("Feishu responder does not expose handoff posting")
	}

	messageID, err := poster.PostHandoff(context.Background(), `<at user_id="ou_guard">Guard</at> 请审核`)
	if err != nil {
		t.Fatal(err)
	}
	if messageID != "om_handoff" {
		t.Fatalf("message id = %q, want om_handoff", messageID)
	}
	var request struct {
		Content       string `json:"content"`
		MsgType       string `json:"msg_type"`
		ReplyInThread bool   `json:"reply_in_thread"`
	}
	if err := json.Unmarshal([]byte(requestBody), &request); err != nil {
		t.Fatalf("decode handoff request: %v", err)
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(request.Content), &content); err != nil {
		t.Fatalf("decode handoff content: %v", err)
	}
	if request.MsgType != "text" || !request.ReplyInThread ||
		content.Text != `<at user_id="ou_guard">Guard</at> 请审核` {
		t.Fatalf("handoff request is not a threaded text mention: %s", requestBody)
	}
}

func TestFeishuResponderMentionsSenderOnlyOnComplete(t *testing.T) {
	var postedCards []string
	var deleted bool
	mockHTTP := &feishuMockHTTP{do: func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			return feishuJSONResponse(`{"code":0,"msg":"success","tenant_access_token":"token","expire":7200}`), nil
		case req.Method == http.MethodPost && req.URL.Path == "/open-apis/im/v1/messages/om_root/reply":
			postedCards = append(postedCards, feishuRequestCardMarkdown(t, req))
			messageID := "om_progress"
			if len(postedCards) > 1 {
				messageID = "om_final"
			}
			return feishuJSONResponse(`{"code":0,"msg":"success","data":{"message_id":"` + messageID + `"}}`), nil
		case req.Method == http.MethodDelete && req.URL.Path == "/open-apis/im/v1/messages/om_progress":
			deleted = true
			return feishuJSONResponse(`{"code":0,"msg":"success","data":{}}`), nil
		default:
			t.Fatalf("unexpected Feishu request: %s %s", req.Method, req.URL.String())
			return nil, nil
		}
	}}
	f := NewFeishuConnector(FeishuConfig{AppID: "app", AppSecret: "secret"})
	f.rest = lark.NewClient("app", "secret", lark.WithHttpClient(mockHTTP))
	responder := f.Responder(Message{MessageID: "om_root", SenderID: "ou_alice"})

	if err := responder.Start(context.Background(), "working"); err != nil {
		t.Fatal(err)
	}
	if err := responder.Complete(context.Background(), "done"); err != nil {
		t.Fatal(err)
	}

	if len(postedCards) != 2 {
		t.Fatalf("posted cards = %d, want progress then final answer", len(postedCards))
	}
	if strings.Contains(postedCards[0], "<at ") {
		t.Fatalf("progress card unexpectedly mentioned sender: %s", postedCards[0])
	}
	if !deleted {
		t.Fatal("progress card was not deleted before the final answer")
	}
	if !strings.Contains(postedCards[1], `<at id="ou_alice"></at> done`) {
		t.Fatalf("final card did not mention sender: %s", postedCards[1])
	}
}

// A 话题 participant the agent addresses by name must reach the card as a real
// at-tag; someone who never appeared in it must stay plain text.
func TestFeishuResponderResolvesThreadParticipantOnComplete(t *testing.T) {
	var card string
	mockHTTP := &feishuMockHTTP{do: func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			return feishuJSONResponse(`{"code":0,"msg":"success","tenant_access_token":"token","expire":7200}`), nil
		case req.Method == http.MethodPost && req.URL.Path == "/open-apis/im/v1/messages/om_root/reply":
			card = feishuRequestCardMarkdown(t, req)
			return feishuJSONResponse(`{"code":0,"msg":"success","data":{"message_id":"om_final"}}`), nil
		default:
			t.Fatalf("unexpected Feishu request: %s %s", req.Method, req.URL.String())
			return nil, nil
		}
	}}
	f := NewFeishuConnector(FeishuConfig{AppID: "app", AppSecret: "secret"})
	f.rest = lark.NewClient("app", "secret", lark.WithHttpClient(mockHTTP))
	f.mentions.remember(mentionThreadKey("oc_chat", "omt_thread"), "吕广超", "ou_louis")

	msg := Message{MessageID: "om_root", ChannelID: "oc_chat", ThreadID: "omt_thread", SenderID: "ou_alice"}
	if err := f.Responder(msg).Complete(context.Background(), "@吕广超 已确认，@关立志 未参与"); err != nil {
		t.Fatal(err)
	}
	want := `<at id="ou_alice"></at> <at id="ou_louis"></at> 已确认，@关立志 未参与`
	if !strings.Contains(card, want) {
		t.Fatalf("final card = %s, want it to contain %s", card, want)
	}
}

func TestFeishuResponderDoesNotMentionInvalidSender(t *testing.T) {
	var card string
	mockHTTP := &feishuMockHTTP{do: func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return feishuJSONResponse(`{"code":0,"msg":"success","tenant_access_token":"token","expire":7200}`), nil
		case "/open-apis/im/v1/messages/om_root/reply":
			card = feishuRequestCardMarkdown(t, req)
			return feishuJSONResponse(`{"code":0,"msg":"success","data":{"message_id":"om_final"}}`), nil
		default:
			t.Fatalf("unexpected Feishu request: %s", req.URL.String())
			return nil, nil
		}
	}}
	f := NewFeishuConnector(FeishuConfig{AppID: "app", AppSecret: "secret"})
	f.rest = lark.NewClient("app", "secret", lark.WithHttpClient(mockHTTP))
	responder := f.Responder(Message{MessageID: "om_root", SenderID: `ou_alice"></at><at id="all`})

	if err := responder.Complete(context.Background(), "done"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(card, "<at ") || card != "done" {
		t.Fatalf("invalid sender id affected final card: %s", card)
	}
}

func feishuRequestCardMarkdown(t *testing.T, req *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode Feishu request: %v", err)
	}
	var card struct {
		Body struct {
			Elements []struct {
				Content string `json:"content"`
			} `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(payload.Content), &card); err != nil {
		t.Fatalf("decode Feishu card: %v", err)
	}
	if len(card.Body.Elements) != 1 {
		t.Fatalf("Feishu card elements = %d, want 1", len(card.Body.Elements))
	}
	return card.Body.Elements[0].Content
}

func TestFeishuHandleReceiveImage(t *testing.T) {
	f := NewFeishuConnector(FeishuConfig{})
	got := collectFeishuMessages(f)
	f.handleReceive(context.Background(), feishuTextEvent(func(e *larkim.P2MessageReceiveV1) {
		e.Event.Message.MessageType = sp("image")
		e.Event.Message.Content = sp(`{"image_key":"img_v2_k1"}`)
	}))
	if len(*got) != 1 {
		t.Fatalf("got %d messages, want 1", len(*got))
	}
	msg := (*got)[0]
	if len(msg.Attachments) != 1 || msg.Attachments[0].ID != "img_v2_k1" || msg.Attachments[0].Download == nil {
		t.Fatalf("image attachment not normalized: %+v", msg.Attachments)
	}
}

func TestFeishuHandleReceiveFile(t *testing.T) {
	f := NewFeishuConnector(FeishuConfig{})
	got := collectFeishuMessages(f)
	f.handleReceive(context.Background(), feishuTextEvent(func(e *larkim.P2MessageReceiveV1) {
		e.Event.Message.MessageType = sp("file")
		e.Event.Message.Content = sp(`{"file_key":"file_v2_k1","file_name":"brief.pdf"}`)
	}))
	if len(*got) != 1 {
		t.Fatalf("got %d messages, want 1", len(*got))
	}
	attachment := (*got)[0].Attachments[0]
	if attachment.ID != "file_v2_k1" || attachment.Name != "brief.pdf" || attachment.Download == nil {
		t.Fatalf("file attachment not normalized: %+v", attachment)
	}
}

func TestFeishuResponderUploadsFileIntoOriginThread(t *testing.T) {
	var uploaded, replyBody string
	mockHTTP := &feishuMockHTTP{do: func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return feishuJSONResponse(`{"code":0,"msg":"success","tenant_access_token":"token","expire":7200}`), nil
		case "/open-apis/im/v1/files":
			if err := req.ParseMultipartForm(1 << 20); err != nil {
				t.Fatal(err)
			}
			file, _, err := req.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(file)
			_ = file.Close()
			uploaded = string(body)
			return feishuJSONResponse(`{"code":0,"msg":"success","data":{"file_key":"file_v2_out"}}`), nil
		case "/open-apis/im/v1/messages/om_root/reply":
			body, _ := io.ReadAll(req.Body)
			replyBody = string(body)
			return feishuJSONResponse(`{"code":0,"msg":"success","data":{"message_id":"om_file"}}`), nil
		default:
			t.Fatalf("unexpected Feishu request: %s", req.URL.String())
			return nil, nil
		}
	}}
	f := NewFeishuConnector(FeishuConfig{AppID: "app", AppSecret: "secret"})
	f.rest = lark.NewClient("app", "secret", lark.WithHttpClient(mockHTTP))
	responder := f.Responder(Message{MessageID: "om_root"})
	uploader, ok := responder.(ArtifactResponder)
	if !ok {
		t.Fatal("Feishu responder does not expose artifact uploads")
	}
	err := uploader.PostAttachments(context.Background(), []OutboundAttachment{{
		Name: "report.pdf", MIME: "application/pdf", Size: 6,
		Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("report")), nil },
	}})
	if err != nil {
		t.Fatal(err)
	}
	if uploaded != "report" {
		t.Fatalf("uploaded content = %q", uploaded)
	}
	var reply struct {
		Content       string `json:"content"`
		MsgType       string `json:"msg_type"`
		ReplyInThread bool   `json:"reply_in_thread"`
	}
	if err := json.Unmarshal([]byte(replyBody), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.MsgType != "file" || !reply.ReplyInThread || !strings.Contains(reply.Content, "file_v2_out") {
		t.Fatalf("unexpected file reply: %s", replyBody)
	}
}

func TestFeishuHandleReceivePost(t *testing.T) {
	f := NewFeishuConnector(FeishuConfig{})
	f.botOpenID = "ou_bot"
	got := collectFeishuMessages(f)
	f.handleReceive(context.Background(), feishuTextEvent(func(e *larkim.P2MessageReceiveV1) {
		e.Event.Message.MessageType = sp("post")
		e.Event.Message.Content = sp(`{
			"title":"发布报告",
			"content":[
				[{"tag":"at","user_id":"ou_bot","user_name":"DayMug"},{"tag":"text","text":"这是截图："}],
				[{"tag":"img","image_key":"img_1"}],
				[{"tag":"img","image_key":"img_2"}]
			]
		}`)
	}))
	if len(*got) != 1 {
		t.Fatalf("got %d messages, want 1", len(*got))
	}
	msg := (*got)[0]
	if !msg.Mentioned {
		t.Fatal("post at-element mention not detected")
	}
	if msg.Text != "发布报告\n这是截图：" {
		t.Fatalf("post text = %q", msg.Text)
	}
	if len(msg.Attachments) != 2 || msg.Attachments[0].ID != "img_1" || msg.Attachments[1].ID != "img_2" {
		t.Fatalf("post images not extracted: %+v", msg.Attachments)
	}
}

func TestParseFeishuPost(t *testing.T) {
	t.Run("links, at-others, and title", func(t *testing.T) {
		text, images, mentioned := parseFeishuPost(`{
			"title":"周报",
			"content":[
				[{"tag":"text","text":"参考 "},{"tag":"a","text":"文档","href":"https://doc.test"}],
				[{"tag":"at","user_id":"ou_x","user_name":"小王"},{"tag":"text","text":" 请确认"}]
			]
		}`, "ou_bot")
		if mentioned {
			t.Fatal("at of another user must not count as bot mention")
		}
		if text != "周报\n参考 文档 (https://doc.test)\n@小王 请确认" {
			t.Fatalf("text = %q", text)
		}
		if len(images) != 0 {
			t.Fatalf("images = %v", images)
		}
	})

	// An unknown bot open_id used to claim every @ as a mention of the bot,
	// which silently defeated require_mention: any message that @-ed a
	// colleague would wake the bot. Start now refuses to run without an
	// identity, so this state is unreachable in production — and where it can
	// still be reached (a zero-value connector) the safe answer is "not me".
	t.Run("unknown bot id claims no mention", func(t *testing.T) {
		text, _, mentioned := parseFeishuPost(`{"content":[[{"tag":"at","user_id":"ou_x","user_name":"小王"}]]}`, "")
		if mentioned {
			t.Fatal("an unknown bot open_id must not claim someone else's @ as its own")
		}
		if text != "@小王" {
			t.Fatalf("text = %q, want the other person's mention inlined", text)
		}
	})

	t.Run("malformed content", func(t *testing.T) {
		text, images, mentioned := parseFeishuPost("not json", "ou_bot")
		if text != "" || images != nil || mentioned {
			t.Fatalf("malformed post should yield zero values: %q %v %v", text, images, mentioned)
		}
	})
}

// Trimmed from a real AWS alert card fetched with raw_card_content: the card
// arrives string-encoded under json_card with text nested in property.
func TestParseFeishuInteractiveRawJSONCard(t *testing.T) {
	card := `{"header":{"property":{"template":"blue","title":{"property":{"content":"🔴 AWS 告警 ALARM｜api02-Memory-High"},"tag":"plain_text"}},"tag":"card_header"},` +
		`"body":{"elements":[{"property":{"text":{"property":{"elements":[` +
		`{"property":{"content":"区域：us-east-2"},"tag":"plain_text"},{"property":{},"tag":"br"},` +
		`{"property":{"content":"状态：OK → ALARM"},"tag":"plain_text"}],"originTag":"lark_md"},"tag":"markdown"}},"tag":"div"},` +
		`{"property":{"elements":[{"property":{"content":"来源：AWS 统一告警接入"},"tag":"plain_text"}]},"tag":"note"}]}}`
	wrapped, err := json.Marshal(map[string]string{"json_card": card})
	if err != nil {
		t.Fatal(err)
	}
	want := "🔴 AWS 告警 ALARM｜api02-Memory-High\n区域：us-east-2\n状态：OK → ALARM\n来源：AWS 统一告警接入"
	if got := parseFeishuInteractive(string(wrapped)); got != want {
		t.Fatalf("parseFeishuInteractive = %q, want %q", got, want)
	}
}

func TestFeishuUserNameUsesCache(t *testing.T) {
	f := NewFeishuConnector(FeishuConfig{})
	if got := f.userName(context.Background(), "ou_x"); got != "" {
		t.Fatalf("nil rest client should resolve to empty, got %q", got)
	}
	// A cached entry must be returned without touching the contact API even
	// when a rest client exists (a live call would need the network).
	f.rest = lark.NewClient("app", "secret")
	f.nameCache["ou_x"] = "小王"
	if got := f.userName(context.Background(), "ou_x"); got != "小王" {
		t.Fatalf("cached name not used, got %q", got)
	}
}

// fakeFeishuWS stands in for *larkws.Client, including the part that caused the
// leak: Start parks for the whole life of the connection and Close is the only
// thing that ends it.
type fakeFeishuWS struct {
	mu      sync.Mutex
	started bool
	closed  bool
	release chan struct{}
}

func newFakeFeishuWS() *fakeFeishuWS { return &fakeFeishuWS{release: make(chan struct{})} }

func (w *fakeFeishuWS) Start(context.Context) error {
	w.mu.Lock()
	w.started = true
	w.mu.Unlock()
	<-w.release
	return nil
}

func (w *fakeFeishuWS) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	close(w.release)
}

func (w *fakeFeishuWS) state() (bool, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.started, w.closed
}

func feishuBotInfoStub(t *testing.T) *feishuMockHTTP {
	t.Helper()
	return &feishuMockHTTP{do: func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return feishuJSONResponse(`{"code":0,"msg":"success","tenant_access_token":"token","expire":7200}`), nil
		case "/open-apis/bot/v3/info":
			return feishuJSONResponse(`{"code":0,"msg":"ok","bot":{"open_id":"ou_bot"}}`), nil
		}
		t.Errorf("unexpected request to %s", req.URL.Path)
		return nil, fmt.Errorf("unexpected request %s", req.URL.Path)
	}}
}

// TestFeishuStopClosesLongConnection is the core of the leak fix: 飞书 caps how
// many long connections one app may hold, and the SDK ignores the context it is
// started with, so every abandoned connector used to keep its socket — and its
// message sink — alive until the app hit that cap and went silent for good.
func TestFeishuStopClosesLongConnection(t *testing.T) {
	ws := newFakeFeishuWS()
	f := NewFeishuConnector(FeishuConfig{Enabled: true, AppID: "app", AppSecret: "secret"})
	f.rest = lark.NewClient("app", "secret", lark.WithHttpClient(feishuBotInfoStub(t)))
	f.nameCache["ou_alice"] = "Alice" // so delivery never reaches the contact API
	f.newWS = func(*dispatcher.EventDispatcher) feishuWSClient { return ws }

	var got []Message
	f.SetOnMessage(func(m Message) { got = append(got, m) })
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("start feishu connector: %v", err)
	}
	eventually(t, func() bool { started, _ := ws.state(); return started }, "long connection was never opened")

	f.handleReceive(context.Background(), feishuTextEvent(nil))
	if len(got) != 1 {
		t.Fatalf("live connector delivered %d messages, want 1", len(got))
	}

	f.Stop()
	if _, closed := ws.state(); !closed {
		t.Fatal("Stop left the 长连接 open")
	}

	f.handleReceive(context.Background(), feishuTextEvent(func(e *larkim.P2MessageReceiveV1) {
		e.Event.Message.MessageId = sp("om_2")
	}))
	if len(got) != 1 {
		t.Fatalf("stopped connector still dispatched %d messages", len(got))
	}

	// Teardown races reconnects: Stop must tolerate being called again, and on a
	// connector whose Start never ran.
	f.Stop()
	NewFeishuConnector(FeishuConfig{}).Stop()
}

// TestFeishuStartRejectsEmptyBotOpenID pins the identity precondition. The bot
// info endpoint answers code:0 with no bot.open_id when the app never enabled
// its bot capability. Starting anyway used to leave botOpenID empty, and the
// mention gate then counted every @-anyone as an @-us — a require_mention
// channel would answer every message in the group.
func TestFeishuStartRejectsEmptyBotOpenID(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"missing open_id", `{"code":0,"msg":"ok","bot":{}}`},
		{"blank open_id", `{"code":0,"msg":"ok","bot":{"open_id":"   "}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mockHTTP := &feishuMockHTTP{do: func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/open-apis/auth/v3/tenant_access_token/internal":
					return feishuJSONResponse(`{"code":0,"msg":"success","tenant_access_token":"token","expire":7200}`), nil
				case "/open-apis/bot/v3/info":
					return feishuJSONResponse(tc.body), nil
				}
				t.Fatalf("unexpected request to %s", req.URL.Path)
				return nil, nil
			}}
			f := NewFeishuConnector(FeishuConfig{Enabled: true, AppID: "app", AppSecret: "secret"})
			f.rest = lark.NewClient("app", "secret", lark.WithHttpClient(mockHTTP))

			if err := f.Start(context.Background()); err == nil {
				t.Fatal("Start accepted an app that reported no bot open_id")
			}
			if f.botOpenID != "" {
				t.Fatalf("botOpenID = %q, want the identity left unset", f.botOpenID)
			}
		})
	}
}

// TestFeishuMentionGateFailsClosedWithoutIdentity is the second half: even if a
// connector somehow reaches the mention gate without an identity, it must not
// claim a colleague's @ as its own.
func TestFeishuMentionGateFailsClosedWithoutIdentity(t *testing.T) {
	f := NewFeishuConnector(FeishuConfig{Enabled: true})
	text, mentioned := f.inlineFeishuMentions("@_user_1 看一下", []feishuMention{
		{key: "@_user_1", openID: "ou_someone_else", name: "小王"},
	})
	if mentioned {
		t.Fatal("a connector with no identity claimed someone else's mention as its own")
	}
	if text != "@小王 看一下" {
		t.Fatalf("text = %q, want the other person's mention inlined", text)
	}
}

// Same doubled-mention bug as the Slack twin: the courtesy prefix must not
// stack on the at-tag resolveMentions already built for the same sender.
func TestFeishuResponderMentionsSenderOnceWhenTheAgentAddressesThem(t *testing.T) {
	var card string
	mockHTTP := &feishuMockHTTP{do: func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			return feishuJSONResponse(`{"code":0,"msg":"success","tenant_access_token":"token","expire":7200}`), nil
		case req.Method == http.MethodPost && req.URL.Path == "/open-apis/im/v1/messages/om_root/reply":
			card = feishuRequestCardMarkdown(t, req)
			return feishuJSONResponse(`{"code":0,"msg":"success","data":{"message_id":"om_final"}}`), nil
		default:
			t.Fatalf("unexpected Feishu request: %s %s", req.Method, req.URL.String())
			return nil, nil
		}
	}}
	f := NewFeishuConnector(FeishuConfig{AppID: "app", AppSecret: "secret"})
	f.rest = lark.NewClient("app", "secret", lark.WithHttpClient(mockHTTP))
	f.mentions.remember(mentionThreadKey("oc_chat", "omt_thread"), "吕广超", "ou_louis")

	msg := Message{MessageID: "om_root", ChannelID: "oc_chat", ThreadID: "omt_thread", SenderID: "ou_louis"}
	if err := f.Responder(msg).Complete(context.Background(), "@吕广超 已确认"); err != nil {
		t.Fatal(err)
	}
	if want := `<at id="ou_louis"></at> 已确认`; card != want {
		t.Fatalf("final card = %s, want %s", card, want)
	}
}
