package imbot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

func newTestSlackConnector() (*SlackConnector, *[]Message) {
	s := &SlackConnector{
		botUserID: "UBOT",
		// Pre-populated so userName never reaches the (absent) Slack API.
		baseConnector: baseConnector{nameCache: map[string]string{"U1": "Alice"}},
	}
	var got []Message
	s.SetOnMessage(func(m Message) { got = append(got, m) })
	return s, &got
}

func TestSlackHandleMessageNormalizes(t *testing.T) {
	s, got := newTestSlackConnector()
	s.handleMessage(context.Background(), &slackevents.MessageEvent{
		Channel: "C1", ChannelType: "channel", User: "U1",
		Text: "<@UBOT> deploy", TimeStamp: "171.100",
	})
	if len(*got) != 1 {
		t.Fatalf("got %d messages, want 1", len(*got))
	}
	msg := (*got)[0]
	if msg.ThreadID != "171.100" || msg.MessageID != "C1|171.100" {
		t.Fatalf("top-level message must root its own thread: %+v", msg)
	}
	if !msg.Mentioned || msg.Text != "deploy" || msg.SenderName != "Alice" || msg.IsDM {
		t.Fatalf("unexpected message: %+v", msg)
	}
}

// newSlackUserInfoServer answers users.info for the ids the mention tests use
// and fails for every other id, so the unresolved-name fallback is exercised
// through the real client path.
func newSlackUserInfoServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users.info" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		switch r.Form.Get("user") {
		case "U2":
			_, _ = w.Write([]byte(`{"ok":true,"user":{"id":"U2","profile":{"display_name":"Bob Chen"}}}`))
		case "W3":
			_, _ = w.Write([]byte(`{"ok":true,"user":{"id":"W3","real_name":"Carol"}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":false,"error":"user_not_found"}`))
		}
	}))
}

func TestSlackInlinesOtherPeoplesMentions(t *testing.T) {
	server := newSlackUserInfoServer(t)
	defer server.Close()

	s, got := newTestSlackConnector()
	s.api = slack.New("test-token", slack.OptionAPIURL(server.URL+"/"))
	s.handleMessage(context.Background(), &slackevents.MessageEvent{
		Channel: "C1", ChannelType: "channel", User: "U1",
		// Mixes the bot's own mention, a bare mention, the legacy labelled
		// form, an enterprise (W) id, an unresolvable id and plain text.
		Text:      "<@UBOT> ask <@U2> and <@W3|carol> to review, cc <@U9> in <#C7|general>",
		TimeStamp: "171.100",
	})

	if len(*got) != 1 {
		t.Fatalf("got %d messages, want 1", len(*got))
	}
	msg := (*got)[0]
	want := "ask @Bob Chen and @Carol to review, cc @U9 in <#C7|general>"
	if msg.Text != want {
		t.Fatalf("text = %q, want %q", msg.Text, want)
	}
	// Rewriting the text must not disturb the mention gate that require_mention
	// keys off; it is computed from the raw event text.
	if !msg.Mentioned {
		t.Fatal("bot mention lost after inlining")
	}
}

func TestSlackAppMentionInlinesOtherPeoplesMentions(t *testing.T) {
	server := newSlackUserInfoServer(t)
	defer server.Close()

	s, got := newTestSlackConnector()
	s.api = slack.New("test-token", slack.OptionAPIURL(server.URL+"/"))
	s.handleAppMention(context.Background(), &slackevents.AppMentionEvent{
		Channel: "C1", User: "U1", Text: "<@UBOT> sync with <@U2>", TimeStamp: "171.100",
	})
	if len(*got) != 1 {
		t.Fatalf("got %d messages, want 1", len(*got))
	}
	if msg := (*got)[0]; msg.Text != "sync with @Bob Chen" || !msg.Mentioned {
		t.Fatalf("unexpected app_mention message: %+v", msg)
	}
}

func TestSlackHandleMessageThreadReply(t *testing.T) {
	s, got := newTestSlackConnector()
	s.handleMessage(context.Background(), &slackevents.MessageEvent{
		Channel: "C1", ChannelType: "channel", User: "U1",
		Text: "follow up", TimeStamp: "171.200", ThreadTimeStamp: "171.100",
	})
	if len(*got) != 1 || (*got)[0].ThreadID != "171.100" {
		t.Fatalf("thread_ts must win over ts: %+v", *got)
	}
	if (*got)[0].Mentioned {
		t.Fatal("plain reply must not count as mention")
	}
}

func TestSlackThreadReplyLoadsMessagesBetweenMentions(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/conversations.replies" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.Form.Get("channel"); got != "C1" {
			t.Errorf("channel = %q, want C1", got)
		}
		if got := r.Form.Get("ts"); got != "171.100" {
			t.Errorf("ts = %q, want thread root", got)
		}
		if got := r.Form.Get("latest"); got != "171.300" {
			t.Errorf("latest = %q, want triggering message ts", got)
		}
		if got := r.Form.Get("oldest"); got != "171.150" {
			t.Errorf("oldest = %q, want previous synced message ts", got)
		}
		if got := r.Form.Get("limit"); got != "100" {
			t.Errorf("limit = %q, want page size 100", got)
		}
		switch calls {
		case 1:
			if got := r.Form.Get("cursor"); got != "" {
				t.Errorf("first cursor = %q, want empty", got)
			}
			_, _ = w.Write([]byte(`{"ok":true,"has_more":true,"response_metadata":{"next_cursor":"next"},"messages":[
				{"user":"U1","text":"root question","ts":"171.100"},
				{"user":"U1","text":"already synced","ts":"171.150"},
				{"user":"U1","text":"<@U1> missed detail","ts":"171.180"}
			]}`))
		case 2:
			if got := r.Form.Get("cursor"); got != "next" {
				t.Errorf("second cursor = %q, want next", got)
			}
			_, _ = w.Write([]byte(`{"ok":true,"messages":[
				{"bot_id":"B1","username":"Helper","text":"an external answer","ts":"171.200"},
				{"user":"UBOT","bot_id":"SELF","text":"our prior reply","ts":"171.250"},
				{"user":"U1","text":"<@UBOT> current","ts":"171.300"}
			]}`))
		default:
			t.Fatalf("unexpected conversations.replies call %d", calls)
		}
	}))
	defer server.Close()

	s, got := newTestSlackConnector()
	s.api = slack.New("test-token", slack.OptionAPIURL(server.URL+"/"))
	s.handleMessage(context.Background(), &slackevents.MessageEvent{
		Channel: "C1", ChannelType: "channel", User: "U1",
		Text: "<@UBOT> current", TimeStamp: "171.300", ThreadTimeStamp: "171.100",
	})
	if len(*got) != 1 || (*got)[0].LoadThreadMessages == nil {
		t.Fatalf("thread reply did not carry a history loader: %+v", *got)
	}
	history, err := (*got)[0].LoadThreadMessages(context.Background(), "C1|171.150")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history = %+v, want two messages before trigger", history)
	}
	// Backfilled history goes through the same mention inliner as live messages.
	if history[0].Text != "@Alice missed detail" || history[0].SenderName != "Alice" || history[0].FromBot {
		t.Fatalf("human history message = %+v", history[0])
	}
	if history[1].Text != "an external answer" || history[1].SenderName != "Helper" || !history[1].FromBot {
		t.Fatalf("bot history message = %+v", history[1])
	}
	if calls != 2 {
		t.Fatalf("history pagination calls = %d, want 2", calls)
	}
}

func TestSlackHandleMessageDM(t *testing.T) {
	s, got := newTestSlackConnector()
	s.handleMessage(context.Background(), &slackevents.MessageEvent{
		Channel: "D1", ChannelType: "im", User: "U1", Text: "hi", TimeStamp: "1.1",
	})
	if len(*got) != 1 || !(*got)[0].IsDM {
		t.Fatalf("DM not detected: %+v", *got)
	}
}

func TestSlackHandleMessageSkips(t *testing.T) {
	tests := []struct {
		name string
		ev   slackevents.MessageEvent
	}{
		{"own echo", slackevents.MessageEvent{Channel: "C1", User: "UBOT", Text: "x", TimeStamp: "1.1"}},
		{"anonymous non-bot event", slackevents.MessageEvent{Channel: "C1", Text: "x", TimeStamp: "1.1"}},
		{"subtype (edit)", slackevents.MessageEvent{Channel: "C1", User: "U1", SubType: "message_changed", Text: "x", TimeStamp: "1.1"}},
		{"empty text without files", slackevents.MessageEvent{Channel: "C1", User: "U1", Text: "  ", TimeStamp: "1.1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, got := newTestSlackConnector()
			ev := tt.ev
			s.handleMessage(context.Background(), &ev)
			if len(*got) != 0 {
				t.Fatalf("expected drop, got %+v", *got)
			}
		})
	}
}

func TestSlackHandleMessageFromOtherBot(t *testing.T) {
	s, got := newTestSlackConnector()
	s.nameCache["UOTHER"] = "ReviewBot"
	s.handleMessage(context.Background(), &slackevents.MessageEvent{
		Channel: "C1", ChannelType: "channel", User: "UOTHER", BotID: "B99",
		Text: "<@UBOT> 请审核", TimeStamp: "171.300",
	})
	if len(*got) != 1 {
		t.Fatalf("other bot's message must pass through, got %d", len(*got))
	}
	msg := (*got)[0]
	if !msg.FromBot || !msg.Mentioned || msg.Text != "请审核" || msg.SenderName != "ReviewBot" {
		t.Fatalf("unexpected bot message: %+v", msg)
	}

	// A bot message with no user id still identifies the sender via bot id.
	s.handleMessage(context.Background(), &slackevents.MessageEvent{
		Channel: "C1", ChannelType: "channel", BotID: "B77", Text: "hello", TimeStamp: "171.400",
	})
	if len(*got) != 2 || !(*got)[1].FromBot || (*got)[1].SenderID != "B77" {
		t.Fatalf("user-less bot message not normalized: %+v", *got)
	}
}

// TestSlackResolvesThreadParticipantOnComplete covers the round trip an agent
// actually needs: someone opened the thread, the agent was pulled in later by a
// different person, and its answer has to notify the thread opener by name.
func TestSlackResolvesThreadParticipantOnComplete(t *testing.T) {
	var posted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/conversations.replies":
			_, _ = w.Write([]byte(`{"ok":true,"messages":[
				{"user":"U2","text":"看一下是不是 CloudFront 出故障了","ts":"171.100"},
				{"bot_id":"B1","username":"Helper","text":"an external answer","ts":"171.200"},
				{"user":"U1","text":"<@UBOT> aws 命令行检查","ts":"171.300"}
			]}`))
		case "/users.info":
			if r.Form.Get("user") == "U2" {
				_, _ = w.Write([]byte(`{"ok":true,"user":{"id":"U2","profile":{"display_name":"吕广超 Louis"}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":false,"error":"user_not_found"}`))
		case "/chat.postMessage":
			posted = append(posted, r.Form.Get("text"))
			_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"200.1"}`))
		case "/assistant.threads.setStatus":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s, got := newTestSlackConnector()
	s.api = slack.New("test-token", slack.OptionAPIURL(server.URL+"/"))
	s.handleMessage(context.Background(), &slackevents.MessageEvent{
		Channel: "C1", ChannelType: "channel", User: "U1",
		Text: "<@UBOT> aws 命令行检查", TimeStamp: "171.300", ThreadTimeStamp: "171.100",
	})
	if len(*got) != 1 {
		t.Fatalf("got %d messages, want 1", len(*got))
	}
	msg := (*got)[0]
	if _, err := msg.LoadThreadMessages(context.Background(), ""); err != nil {
		t.Fatal(err)
	}

	responder := s.Responder(msg)
	// "@Helper" names the other bot in the thread: ordinary prose must not be
	// able to trigger it, that is what the explicit HANDOFF protocol is for.
	if err := responder.Complete(context.Background(), "@吕广超 Louis 已确认，不必找 @Helper"); err != nil {
		t.Fatal(err)
	}
	if len(posted) != 1 {
		t.Fatalf("posted = %q, want one final answer", posted)
	}
	want := "<@U1> <@U2> 已确认，不必找 @Helper"
	if posted[0] != want {
		t.Fatalf("final text = %q, want %q", posted[0], want)
	}
}

func TestSlackDoesNotResolveMentionsAcrossThreads(t *testing.T) {
	var posted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/chat.postMessage":
			posted = append(posted, r.Form.Get("text"))
			_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"200.1"}`))
		case "/assistant.threads.setStatus":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s, got := newTestSlackConnector()
	s.api = slack.New("test-token", slack.OptionAPIURL(server.URL+"/"))
	s.handleMessage(context.Background(), &slackevents.MessageEvent{
		Channel: "C1", ChannelType: "channel", User: "U1", Text: "hi", TimeStamp: "171.100",
	})
	if len(*got) != 1 {
		t.Fatalf("got %d messages, want 1", len(*got))
	}

	// A different thread in the same channel never saw Alice speak.
	responder := s.Responder(Message{ChannelID: "C1", ThreadID: "999.100", SenderID: "U9ZZZ"})
	if err := responder.Complete(context.Background(), "ping @Alice"); err != nil {
		t.Fatal(err)
	}
	if want := "<@U9ZZZ> ping @Alice"; len(posted) != 1 || posted[0] != want {
		t.Fatalf("final text = %q, want %q", posted, want)
	}
}

func TestSlackMentionTag(t *testing.T) {
	if got := (&SlackConnector{}).MentionTag(); got != "" {
		t.Fatalf("unstarted connector must have no mention tag, got %q", got)
	}
	tag := (&SlackConnector{botUserID: "UBOT"}).MentionTag()
	if got, want := tag, slackRawMarker+"<@UBOT>"+slackRawMarker; got != want {
		t.Fatalf("mention tag = %q, want %q", got, want)
	}
	// The tag is composed by DayMug, so it must reach Slack as live markup
	// even though the agent instruction next to it gets escaped.
	if got, want := SlackMrkdwn(tag+" review <!channel>"), "<@UBOT> review &lt;!channel&gt;"; got != want {
		t.Fatalf("handoff text = %q, want %q", got, want)
	}
}

func TestSlackResponderHandoffReplacesProgress(t *testing.T) {
	var posted, deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/chat.postMessage":
			posted = append(posted, r.Form.Get("text"))
			_, _ = fmt.Fprintf(w, `{"ok":true,"channel":"C1","ts":"200.%d"}`, len(posted))
		case "/chat.delete":
			deleted = append(deleted, r.Form.Get("ts"))
			_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"200.1"}`))
		case "/assistant.threads.setStatus":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector := &SlackConnector{api: slack.New("test-token", slack.OptionAPIURL(server.URL+"/")), botUserID: "UBOT"}
	responder := connector.Responder(Message{ChannelID: "C1", ThreadID: "100.1"}).(*slackResponder)
	if err := responder.Start(context.Background(), "thinking"); err != nil {
		t.Fatal(err)
	}
	messageID, err := responder.PostHandoff(context.Background(), "<@UBOT> review")
	if err != nil {
		t.Fatal(err)
	}
	if messageID != "200.2" || len(posted) != 2 || len(deleted) != 1 || deleted[0] != "200.1" {
		t.Fatalf("handoff requests = posts %q, deleted %q, id %q", posted, deleted, messageID)
	}
}

func TestValidateSlackBotIdentityRejectsUserToken(t *testing.T) {
	if err := validateSlackBotIdentity(&slack.AuthTestResponse{UserID: "U123"}); err == nil {
		t.Fatal("user-token auth without bot_id must be rejected")
	}
	if err := validateSlackBotIdentity(&slack.AuthTestResponse{UserID: "U123", BotID: "B123"}); err != nil {
		t.Fatalf("bot identity rejected: %v", err)
	}
}

func TestSlackHandleAppMentionSharesMessageID(t *testing.T) {
	s, got := newTestSlackConnector()
	s.handleAppMention(context.Background(), &slackevents.AppMentionEvent{
		Channel: "C1", User: "U1", Text: "<@UBOT> hi", TimeStamp: "171.100",
	})
	if len(*got) != 1 {
		t.Fatalf("got %d messages, want 1", len(*got))
	}
	mention := (*got)[0]
	if !mention.Mentioned || mention.Text != "hi" {
		t.Fatalf("unexpected mention message: %+v", mention)
	}

	// The duplicate message event for the same post must normalize to the
	// same MessageID so the manager's dedup can collapse the pair.
	s.handleMessage(context.Background(), &slackevents.MessageEvent{
		Channel: "C1", ChannelType: "channel", User: "U1",
		Text: "<@UBOT> hi", TimeStamp: "171.100",
	})
	if len(*got) != 2 || (*got)[0].MessageID != (*got)[1].MessageID {
		t.Fatalf("app_mention and message ids diverge: %+v", *got)
	}
}

func TestSlackResponderSetsAndClearsTypingStatus(t *testing.T) {
	type statusCall struct {
		status   string
		channel  string
		threadTS string
	}
	var mu sync.Mutex
	var statuses []statusCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		switch r.URL.Path {
		case "/chat.postMessage":
			_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"200.1"}`))
		case "/chat.delete":
			_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"200.1"}`))
		case "/chat.update":
			_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"200.1"}`))
		case "/assistant.threads.setStatus":
			mu.Lock()
			statuses = append(statuses, statusCall{
				status: r.Form.Get("status"), channel: r.Form.Get("channel_id"), threadTS: r.Form.Get("thread_ts"),
			})
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector := &SlackConnector{api: slack.New("test-token", slack.OptionAPIURL(server.URL+"/"))}
	responder := connector.Responder(Message{ChannelID: "C1", ThreadID: "171.100"})
	if err := responder.Start(context.Background(), "working"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := responder.Complete(context.Background(), "done"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(statuses) != 2 {
		t.Fatalf("status calls = %+v, want set then clear", statuses)
	}
	if got := statuses[0]; got.status != "正在输入…" || got.channel != "C1" || got.threadTS != "171.100" {
		t.Fatalf("set status = %+v", got)
	}
	if got := statuses[1]; got.status != "" || got.channel != "C1" || got.threadTS != "171.100" {
		t.Fatalf("clear status = %+v", got)
	}
}

func TestSlackResponderMentionsSenderOnlyOnComplete(t *testing.T) {
	var posted []string
	var deleted string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/chat.postMessage":
			posted = append(posted, r.Form.Get("text"))
			_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"200.1"}`))
		case "/chat.delete":
			deleted = r.Form.Get("ts")
			_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"200.1"}`))
		case "/assistant.threads.setStatus":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector := &SlackConnector{api: slack.New("test-token", slack.OptionAPIURL(server.URL+"/"))}
	responder := connector.Responder(Message{ChannelID: "C1", ThreadID: "171.100", SenderID: "U123ABC"})
	if err := responder.Start(context.Background(), "working"); err != nil {
		t.Fatal(err)
	}
	if err := responder.Complete(context.Background(), "done <!channel>"); err != nil {
		t.Fatal(err)
	}

	if len(posted) != 2 || posted[0] != "working" {
		t.Fatalf("posted messages = %q, want progress then final answer", posted)
	}
	if deleted != "200.1" {
		t.Fatalf("deleted progress = %q, want %q", deleted, "200.1")
	}
	if want := "<@U123ABC> done &lt;!channel&gt;"; posted[1] != want {
		t.Fatalf("final text = %q, want %q", posted[1], want)
	}
}

func TestSlackResponderDoesNotMentionInvalidSender(t *testing.T) {
	var posted string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/chat.postMessage":
			posted = r.Form.Get("text")
			_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"200.1"}`))
		case "/assistant.threads.setStatus":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector := &SlackConnector{api: slack.New("test-token", slack.OptionAPIURL(server.URL+"/"))}
	responder := connector.Responder(Message{ChannelID: "C1", ThreadID: "171.100", SenderID: "U1> <!channel"})
	if err := responder.Complete(context.Background(), "done"); err != nil {
		t.Fatal(err)
	}
	if posted != "done" {
		t.Fatalf("final text = %q, want invalid sender id omitted", posted)
	}
}

func TestSlackResponderDoesNotMentionBotSenderOnOrdinaryReply(t *testing.T) {
	var posted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/chat.postMessage":
			posted = append(posted, r.Form.Get("text"))
			_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"200.1"}`))
		case "/assistant.threads.setStatus":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s, _ := newTestSlackConnector()
	s.api = slack.New("test-token", slack.OptionAPIURL(server.URL+"/"))
	responder := s.Responder(Message{
		ChannelID: "C1", ThreadID: "171.100", SenderID: "UOTHERBOT", FromBot: true,
	})

	if err := responder.Complete(context.Background(), "审核完成，等待人工授权"); err != nil {
		t.Fatal(err)
	}
	if want := "审核完成，等待人工授权"; len(posted) != 1 || posted[0] != want {
		t.Fatalf("final text = %q, want %q", posted, want)
	}
}

func TestSlackResponderUploadsArtifactIntoOriginThread(t *testing.T) {
	const artifactName = "品牌包装智能建站系统技术开发项目-技术部分审阅修订.docx"
	artifactPath := t.TempDir() + "/" + artifactName
	if err := os.WriteFile(artifactPath, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}

	var uploaded string
	var completedChannel, completedThread, initialComment string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.getUploadURLExternal":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("filename") != artifactName || r.Form.Get("length") != "6" || r.Form.Get("alt_txt") != "" {
				t.Errorf("unexpected upload request: %v", r.Form)
			}
			_, _ = w.Write([]byte(`{"ok":true,"upload_url":"` + server.URL + `/upload","file_id":"F1"}`))
		case "/upload":
			if got := r.Header.Get("Content-Type"); got != "application/octet-stream" {
				t.Errorf("Content-Type = %q, want application/octet-stream", got)
			}
			if r.ContentLength != 6 {
				t.Errorf("Content-Length = %d, want 6", r.ContentLength)
			}
			body, _ := io.ReadAll(r.Body)
			uploaded = string(body)
			_, _ = w.Write([]byte(`ok`))
		case "/files.completeUploadExternal":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			completedChannel = r.Form.Get("channel_id")
			completedThread = r.Form.Get("thread_ts")
			initialComment = r.Form.Get("initial_comment")
			_, _ = fmt.Fprintf(w, `{"ok":true,"files":[{"id":"F1","title":%q}]}`, artifactName)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector := &SlackConnector{api: slack.New("test-token", slack.OptionAPIURL(server.URL+"/"))}
	responder := connector.Responder(Message{ChannelID: "C1", ThreadID: "171.100"})
	uploader, ok := responder.(ArtifactResponder)
	if !ok {
		t.Fatal("Slack responder does not expose artifact uploads")
	}
	err := uploader.PostAttachments(context.Background(), []OutboundAttachment{{
		Name: artifactName, MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Size: 6,
		Open: func() (io.ReadCloser, error) { return os.Open(artifactPath) },
	}})
	if err != nil {
		t.Fatal(err)
	}
	if uploaded != "report" || completedChannel != "C1" || completedThread != "171.100" || initialComment != artifactName {
		t.Fatalf("upload=%q channel=%q thread=%q comment=%q", uploaded, completedChannel, completedThread, initialComment)
	}
}

func TestSlackResponderRejectsIncompleteUploadConfirmation(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.getUploadURLExternal":
			_, _ = w.Write([]byte(`{"ok":true,"upload_url":"` + server.URL + `/upload","file_id":"F1"}`))
		case "/upload":
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte("ok"))
		case "/files.completeUploadExternal":
			_, _ = w.Write([]byte(`{"ok":true,"files":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector := &SlackConnector{api: slack.New("test-token", slack.OptionAPIURL(server.URL+"/"))}
	uploader := connector.Responder(Message{ChannelID: "C1", ThreadID: "171.100"}).(ArtifactResponder)
	err := uploader.PostAttachments(context.Background(), []OutboundAttachment{{
		Name: "report.docx", Size: 6,
		Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("report")), nil },
	}})
	if err == nil || !strings.Contains(err.Error(), "response omitted file F1") {
		t.Fatalf("PostAttachments error = %v, want missing completed file", err)
	}
}

// TestSlackHandleMessageAcceptsFileShare drives the real wire format through
// the real entry point. Slack delivers an upload as a message carrying the
// file_share subtype, and MessageEvent's custom unmarshaller folds a regular
// message's top-level payload into Message — so both the subtype gate and the
// file lookup have to be exercised against JSON, not a hand-built struct.
func TestSlackHandleMessageAcceptsFileShare(t *testing.T) {
	const payload = `{
		"type": "message",
		"subtype": "file_share",
		"channel": "C1",
		"channel_type": "channel",
		"user": "U1",
		"text": "看看这张图",
		"ts": "1700000000.000100",
		"files": [{
			"id": "F1",
			"name": "shot.png",
			"mimetype": "image/png",
			"size": 1234,
			"url_private_download": "https://files.slack.com/f1"
		}]
	}`

	var ev slackevents.MessageEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("unmarshal file_share payload: %v", err)
	}

	s, got := newTestSlackConnector()
	s.handleMessage(context.Background(), &ev)

	if len(*got) != 1 {
		t.Fatalf("emitted %d messages, want the upload to be delivered", len(*got))
	}
	msg := (*got)[0]
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want the uploaded file", len(msg.Attachments))
	}
	attachment := msg.Attachments[0]
	if attachment.Name != "shot.png" || attachment.MIME != "image/png" || attachment.Size != 1234 {
		t.Errorf("attachment = %+v, want shot.png/image/png/1234", attachment)
	}
	if msg.Text != "看看这张图" {
		t.Errorf("text = %q, want the upload comment preserved", msg.Text)
	}
	if msg.SenderID != "U1" {
		t.Errorf("sender = %q, want U1", msg.SenderID)
	}
}

// TestSlackStopEndsSocketModeRun covers the Slack half of the connection leak.
// The supervisor reuses one context for every attempt of a bot, and socketmode
// never closes its Events channel, so an attempt that is not explicitly stopped
// keeps its event loop — and its socket — for as long as the bot exists. Stop
// returning is the assertion: before the fix nothing could end a run early.
func TestSlackStopEndsSocketModeRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth.test" {
			_, _ = w.Write([]byte(`{"ok":true,"user_id":"UBOT","bot_id":"BBOT","team_id":"T1"}`))
			return
		}
		// Every socket dial fails, the way a revoked app-level token behaves:
		// auth.test still passes, the long connection never comes up.
		_, _ = w.Write([]byte(`{"ok":false,"error":"not_allowed_token_type"}`))
	}))
	t.Cleanup(server.Close)

	s := NewSlackConnector(SlackConfig{Enabled: true, BotToken: "xoxb-test", AppToken: "xapp-test"})
	s.api = slack.New("xoxb-test", slack.OptionAppLevelToken("xapp-test"), slack.OptionAPIURL(server.URL+"/"))
	delivered := make(chan Message, 1)
	s.SetOnMessage(func(msg Message) { delivered <- msg })

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start slack connector: %v", err)
	}

	stopped := make(chan struct{})
	go func() {
		s.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not end the Socket Mode run")
	}

	s.emit(context.Background(), Message{ChannelID: "C1", Text: "late"})
	select {
	case msg := <-delivered:
		t.Fatalf("stopped connector still delivered %+v", msg)
	default:
	}
}

// TestSlackHandleMessageSkipsMutationSubtypes guards the other half of the
// gate: file_share is the only subtype that carries new user content.
func TestSlackHandleMessageSkipsMutationSubtypes(t *testing.T) {
	for _, subtype := range []string{"message_changed", "message_deleted", "channel_join"} {
		t.Run(subtype, func(t *testing.T) {
			s, got := newTestSlackConnector()
			s.handleMessage(context.Background(), &slackevents.MessageEvent{
				SubType: subtype, Channel: "C1", User: "U1", Text: "noise", TimeStamp: "1.1",
			})
			if len(*got) != 0 {
				t.Fatalf("emitted %d messages, want %s to stay silent", len(*got), subtype)
			}
		})
	}
}

// An answer that opens by addressing the asker must ping them once, not twice:
// resolveMentions makes the agent's "@Name" live, so the courtesy prefix has
// nothing left to add. Observed 2026-08-12 in a Slack review thread.
func TestSlackMentionsSenderOnceWhenTheAgentAddressesThem(t *testing.T) {
	var posted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/chat.postMessage":
			posted = append(posted, r.Form.Get("text"))
			_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"200.1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s, got := newTestSlackConnector()
	s.api = slack.New("test-token", slack.OptionAPIURL(server.URL+"/"))
	s.handleMessage(context.Background(), &slackevents.MessageEvent{
		Channel: "C1", ChannelType: "channel", User: "U1",
		Text: "<@UBOT> 合并后发布", TimeStamp: "171.300", ThreadTimeStamp: "171.100",
	})
	if len(*got) != 1 {
		t.Fatalf("got %d messages, want 1", len(*got))
	}

	responder := s.Responder((*got)[0])
	if err := responder.Complete(context.Background(), "@Alice 合并后我会按以下链路发布"); err != nil {
		t.Fatal(err)
	}
	if len(posted) != 1 {
		t.Fatalf("posted = %q, want one final answer", posted)
	}
	if want := "<@U1> 合并后我会按以下链路发布"; posted[0] != want {
		t.Fatalf("final text = %q, want %q", posted[0], want)
	}
}
