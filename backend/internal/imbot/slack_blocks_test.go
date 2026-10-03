package imbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// amazonQAlarmMessage is the shape AWS Chatbot / Amazon Q actually posts: an
// empty top-level text with every fact in Block Kit blocks under a secondary
// attachment. Reading `text` alone produced an empty message that the connector
// dropped, so an operator asking "why did this fire?" got a thread with no alarm
// in it.
const amazonQAlarmMessage = `{"bot_id":"B097","username":"Amazon Q","text":"","ts":"171.100","attachments":[{"color":"#FF0000","fallback":"CloudWatch Alarm | ohio-canary01-CPU-High","blocks":[
	{"type":"section","text":{"type":"mrkdwn","text":"*<https://console.aws.amazon.com|:rotating_light: CloudWatch Alarm | ohio-canary01-CPU-High | us-east-2>*"}},
	{"type":"section","text":{"type":"mrkdwn","text":"Threshold Crossed: 5 datapoints were greater than the threshold (80.0)."}},
	{"type":"actions","elements":[{"type":"button","text":{"type":"plain_text","text":"List dashboards"}}]},
	{"type":"section","fields":[{"type":"mrkdwn","text":"*InstanceId*\ni-02fb27bc30c743106"},{"type":"mrkdwn","text":"*Alarm State*\nALARM"}]}
]}]}`

func TestSlackThreadBackfillRendersBlockOnlyCard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.replies" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"messages":[` + amazonQAlarmMessage + `,
			{"user":"U1","text":"<@UBOT> 检查本条告警的原因","ts":"171.200"}
		]}`))
	}))
	defer server.Close()

	s, got := newTestSlackConnector()
	s.api = slack.New("test-token", slack.OptionAPIURL(server.URL+"/"))
	s.handleMessage(context.Background(), &slackevents.MessageEvent{
		Channel: "C1", ChannelType: "channel", User: "U1",
		Text: "<@UBOT> 检查本条告警的原因", TimeStamp: "171.200", ThreadTimeStamp: "171.100",
	})
	if len(*got) != 1 || (*got)[0].LoadThreadMessages == nil {
		t.Fatalf("thread reply did not carry a history loader: %+v", *got)
	}
	history, err := (*got)[0].LoadThreadMessages(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history = %+v, want the alarm card", history)
	}
	alarm := history[0]
	if !alarm.FromBot || alarm.SenderName != "Amazon Q" {
		t.Fatalf("alarm sender = %+v", alarm)
	}
	for _, want := range []string{"ohio-canary01-CPU-High", "Threshold Crossed", "i-02fb27bc30c743106", "Alarm State", "ALARM"} {
		if !strings.Contains(alarm.Text, want) {
			t.Errorf("alarm text missing %q:\n%s", want, alarm.Text)
		}
	}
	// Buttons are interaction affordances, not facts about the incident.
	if strings.Contains(alarm.Text, "List dashboards") {
		t.Errorf("alarm text kept a button label:\n%s", alarm.Text)
	}
}

func TestSlackLiveBlockOnlyCardIsNotDropped(t *testing.T) {
	var event slackevents.MessageEvent
	if err := json.Unmarshal([]byte(amazonQAlarmMessage), &event); err != nil {
		t.Fatal(err)
	}
	event.Channel = "C1"
	event.ChannelType = "channel"

	s, got := newTestSlackConnector()
	s.handleMessage(context.Background(), &event)
	if len(*got) != 1 {
		t.Fatalf("got %d messages, want the alarm card to survive emit", len(*got))
	}
	if !strings.Contains((*got)[0].Text, "ohio-canary01-CPU-High") || !(*got)[0].FromBot {
		t.Fatalf("unexpected message: %+v", (*got)[0])
	}
}

// TestSlackBotHandoffBackfillsExactlyOnce covers the bot-to-bot relay. Slack
// reports DeliversBotMessagesToBots, so bot B sees bot A's handoff as a native
// bot message and again in thread backfill — both times through the renderer.
// DayMug posts its own replies as plain text (postInThread sends MsgOptionText
// with no blocks), and Slack still hangs a link unfurl off such a message as an
// attachment, so this is exactly the shape that would double if the renderer
// ever appended instead of choosing.
func TestSlackBotHandoffBackfillsExactlyOnce(t *testing.T) {
	const handoff = `{"bot_id":"BA","username":"Ada","text":"<@UBOT> 请复核这次变更 https://example.com/pr/42","ts":"171.150","attachments":[{"title":"PR 42","text":"unfurled preview","fallback":"PR 42"}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.replies" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"messages":[
			{"user":"U1","text":"root","ts":"171.100"},` + handoff + `,
			{"user":"U1","text":"<@UBOT> current","ts":"171.200"}
		]}`))
	}))
	defer server.Close()

	s, got := newTestSlackConnector()
	s.api = slack.New("test-token", slack.OptionAPIURL(server.URL+"/"))
	s.handleMessage(context.Background(), &slackevents.MessageEvent{
		Channel: "C1", ChannelType: "channel", User: "U1",
		Text: "<@UBOT> current", TimeStamp: "171.200", ThreadTimeStamp: "171.100",
	})
	history, err := (*got)[0].LoadThreadMessages(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var relayed []Message
	for _, msg := range history {
		if msg.FromBot {
			relayed = append(relayed, msg)
		}
	}
	if len(relayed) != 1 {
		t.Fatalf("relayed handoff appeared %d times: %+v", len(relayed), relayed)
	}
	// Byte-exact: the unfurl attachment must not have been appended, and the
	// instruction must not appear twice. The bot's own mention tag is stripped
	// by inlineMentions, as it is for every inbound message.
	if want := "请复核这次变更 https://example.com/pr/42"; relayed[0].Text != want {
		t.Fatalf("handoff text = %q, want %q", relayed[0].Text, want)
	}
	if strings.Contains(relayed[0].Text, "unfurled preview") {
		t.Errorf("unfurl attachment leaked into the handoff body: %q", relayed[0].Text)
	}
}

func TestSlackMessageText(t *testing.T) {
	blocks := slack.Blocks{BlockSet: []slack.Block{
		slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, "block copy", false, false), nil, nil),
	}}
	tests := []struct {
		name        string
		text        string
		blocks      slack.Blocks
		attachments []slack.Attachment
		want        string
	}{
		{
			name:   "plain text wins so ordinary messages are not duplicated",
			text:   "hello",
			blocks: blocks,
			want:   "hello",
		},
		{
			// The rendering must stay strictly either/or. A bot handoff carries
			// its instruction in `text` while Slack hangs a link unfurl off the
			// same message as an attachment; appending instead of choosing would
			// feed the receiving agent the same turn twice.
			name:        "text with blocks and attachments never concatenates",
			text:        "<@UBOT2> 看一下 https://example.com/incident",
			blocks:      blocks,
			attachments: []slack.Attachment{{Title: "Incident 42", Text: "unfurled preview", Blocks: blocks}},
			want:        "<@UBOT2> 看一下 https://example.com/incident",
		},
		{
			name:   "top-level blocks fill in for an empty text",
			blocks: blocks,
			want:   "block copy",
		},
		{
			name:        "legacy attachment fields render when there are no blocks",
			attachments: []slack.Attachment{{Pretext: "Deploy", Title: "web #42", Text: "succeeded", Footer: "CI"}},
			want:        "Deploy\nweb #42\nsucceeded\nCI",
		},
		{
			name:        "fallback is the last resort, not an extra copy",
			attachments: []slack.Attachment{{Fallback: "summary only"}},
			want:        "summary only",
		},
		{
			name:        "attachment blocks outrank the fallback that repeats them",
			attachments: []slack.Attachment{{Fallback: "repeated summary", Blocks: blocks}},
			want:        "block copy",
		},
		{
			name: "nothing to render stays empty so the message is still dropped",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := slackMessageText(tc.text, tc.blocks, tc.attachments); got != tc.want {
				t.Fatalf("slackMessageText = %q, want %q", got, tc.want)
			}
		})
	}
}
