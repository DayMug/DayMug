package imbot

// Cross-connector contract for thread-history backfill.
//
// Slack pages conversations.replies with an opaque cursor; 飞书 pages im/messages
// with a page_token plus a create-time window and has to fetch the thread root
// separately. The two implementations are deliberately NOT merged — but the
// bridge treats their output identically, so both must obey the same
// invariants. One script is replayed through both platform stubs (httptest +
// slack.OptionAPIURL for Slack, lark.WithHttpClient for 飞书) and the same
// assertions run over both results.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/slack-go/slack"
)

type contractRole int

const (
	contractHuman contractRole = iota
	// contractSelfBot is a message authored by the connector's own bot.
	contractSelfBot
	// contractOtherBot is a message authored by a foreign app/bot, which is
	// legitimate context and must survive the backfill.
	contractOtherBot
)

type contractScriptMessage struct {
	seq  int
	role contractRole
	text string
}

// contractThread is one thread as both platforms would report it. seq doubles
// as the ordering key and as the id/timestamp seed on each platform.
var contractThread = []contractScriptMessage{
	{seq: 1, role: contractHuman, text: "root question"},
	{seq: 2, role: contractHuman, text: "already synced"},
	{seq: 3, role: contractSelfBot, text: "our own progress note"},
	{seq: 4, role: contractHuman, text: "   "},
	{seq: 5, role: contractOtherBot, text: "an external answer"},
	{seq: 6, role: contractHuman, text: "missed detail"},
	{seq: 7, role: contractHuman, text: "the triggering message"},
}

const (
	contractRootSeq    = 1
	contractSelfSeq    = 3
	contractEmptySeq   = 4
	contractTriggerSeq = 7
	// contractPageSize forces both stubs to paginate, so the invariants also
	// cover the page-stitching path.
	contractPageSize = 3
)

// contractConnector adapts one platform to the shared assertions.
type contractConnector struct {
	name     string
	platform string
	// newLoader stubs the platform API with the script and returns the
	// connector's thread-history loader.
	newLoader func(t *testing.T, script []contractScriptMessage) func(context.Context, string) ([]Message, error)
	// newStuckLoader stubs a platform API whose pagination cursor never
	// advances, so a naive loader would spin forever.
	newStuckLoader func(t *testing.T) func(context.Context, string) ([]Message, error)
	// stuckError is the platform-specific wording the loader must fail with,
	// so the test cannot pass on some unrelated error.
	stuckError string
	// messageID is the bridge-level Message.MessageID for a scripted message —
	// the same value the bridge hands back as afterMessageID.
	messageID func(seq int) string
}

func contractConnectors() []contractConnector {
	return []contractConnector{
		{
			name: "slack", platform: PlatformSlack,
			newLoader: slackContractLoader, newStuckLoader: slackStuckCursorLoader,
			stuckError: "repeated cursor", messageID: slackContractMessageID,
		},
		{
			name: "feishu", platform: PlatformFeishu,
			newLoader: feishuContractLoader, newStuckLoader: feishuStuckPageTokenLoader,
			stuckError: "repeated page token", messageID: feishuContractMessageID,
		},
	}
}

func TestConnectorThreadHistoryContract(t *testing.T) {
	cases := []struct {
		name string
		// afterSeq is the scripted message the bridge already imported; 0 means
		// a full backfill.
		afterSeq int
		want     []string
	}{
		{
			name:     "full backfill keeps every usable message before the trigger",
			afterSeq: 0,
			want:     []string{"root question", "already synced", "an external answer", "missed detail"},
		},
		{
			name:     "cursor skips everything up to and including the anchor",
			afterSeq: 2,
			want:     []string{"an external answer", "missed detail"},
		},
	}

	for _, connector := range contractConnectors() {
		for _, tt := range cases {
			t.Run(connector.name+"/"+tt.name, func(t *testing.T) {
				loader := connector.newLoader(t, contractThread)
				if loader == nil {
					t.Fatal("connector exposed no thread-history loader")
				}
				after := ""
				if tt.afterSeq != 0 {
					after = connector.messageID(tt.afterSeq)
				}
				history, err := loader(context.Background(), after)
				if err != nil {
					t.Fatalf("load thread history: %v", err)
				}

				texts := make([]string, 0, len(history))
				for _, msg := range history {
					texts = append(texts, msg.Text)
				}
				if !slices.Equal(texts, tt.want) {
					t.Fatalf("history texts = %q, want %q", texts, tt.want)
				}

				seqByID := map[string]int{}
				for _, scripted := range contractThread {
					seqByID[connector.messageID(scripted.seq)] = scripted.seq
				}
				previous := 0
				for i, msg := range history {
					seq, known := seqByID[msg.MessageID]
					if !known {
						t.Fatalf("history[%d] has an unscripted message id %q", i, msg.MessageID)
					}
					// Invariant 7: platform tagging.
					if msg.Platform != connector.platform {
						t.Errorf("history[%d].Platform = %q, want %q", i, msg.Platform, connector.platform)
					}
					// Invariant 1: never the bot's own messages.
					if seq == contractSelfSeq {
						t.Errorf("history[%d] leaked the bot's own message", i)
					}
					// Invariant 5: nothing empty of both text and attachments.
					if seq == contractEmptySeq {
						t.Errorf("history[%d] kept a message with neither text nor attachments", i)
					}
					// Invariant 2: strictly before the triggering message.
					if seq >= contractTriggerSeq {
						t.Errorf("history[%d] is not older than the trigger (seq %d)", i, seq)
					}
					// Invariant 3: strictly after the caller's cursor.
					if seq <= tt.afterSeq {
						t.Errorf("history[%d] seq %d is not newer than cursor seq %d", i, seq, tt.afterSeq)
					}
					// Invariant 4: oldest first.
					if seq <= previous {
						t.Errorf("history[%d] seq %d breaks oldest-first order after %d", i, seq, previous)
					}
					previous = seq
				}
			})
		}
	}
}

// TestConnectorThreadHistoryStuckCursorTerminates covers invariant 6: a
// platform that keeps handing back the same pagination cursor must not spin
// the loader forever. `go test` timeouts turn a regression into a failure
// rather than a hang.
func TestConnectorThreadHistoryStuckCursorTerminates(t *testing.T) {
	for _, connector := range contractConnectors() {
		t.Run(connector.name, func(t *testing.T) {
			loader := connector.newStuckLoader(t)
			if loader == nil {
				t.Fatal("connector exposed no thread-history loader")
			}
			_, err := loader(context.Background(), "")
			if err == nil {
				t.Fatal("a repeated pagination cursor must abort the backfill with an error")
			}
			if !strings.Contains(err.Error(), connector.stuckError) {
				t.Fatalf("backfill failed with %v, want a %q guard", err, connector.stuckError)
			}
		})
	}
}

// --- Slack stub ---------------------------------------------------------

func slackContractTS(seq int) string { return fmt.Sprintf("171.%03d", seq) }

func slackContractMessageID(seq int) string { return "C1|" + slackContractTS(seq) }

func slackContractPayload(scripted contractScriptMessage) map[string]any {
	payload := map[string]any{"ts": slackContractTS(scripted.seq), "text": scripted.text}
	switch scripted.role {
	case contractSelfBot:
		payload["user"] = "UBOT"
		payload["bot_id"] = "BSELF"
	case contractOtherBot:
		payload["bot_id"] = "B1"
		payload["username"] = "Helper"
	default:
		payload["user"] = "U1"
	}
	return payload
}

func newSlackContractConnector(t *testing.T, apiURL string) *SlackConnector {
	t.Helper()
	s := &SlackConnector{
		botUserID: "UBOT",
		// Pre-populated so name resolution never reaches the stub server.
		baseConnector: baseConnector{nameCache: map[string]string{"U1": "Alice"}},
	}
	s.api = slack.New("test-token", slack.OptionAPIURL(apiURL+"/"))
	return s
}

func slackContractLoader(t *testing.T, script []contractScriptMessage) func(context.Context, string) ([]Message, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.replies" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse conversations.replies form: %v", err)
			return
		}
		oldest, latest := r.Form.Get("oldest"), r.Form.Get("latest")

		// Mirror the real endpoint: the time window is applied server side, the
		// thread parent is always returned on the first page regardless of it,
		// and `latest` is inclusive.
		visible := make([]map[string]any, 0, len(script))
		for _, scripted := range script {
			ts := slackContractTS(scripted.seq)
			if scripted.seq != contractRootSeq {
				if oldest != "" && ts < oldest {
					continue
				}
				if latest != "" && ts > latest {
					continue
				}
			}
			visible = append(visible, slackContractPayload(scripted))
		}

		offset := 0
		if cursor := r.Form.Get("cursor"); cursor != "" {
			parsed, err := strconv.Atoi(cursor)
			if err != nil {
				t.Errorf("unexpected cursor %q", cursor)
				return
			}
			offset = parsed
		}
		end := min(offset+contractPageSize, len(visible))
		body := map[string]any{"ok": true, "messages": visible[offset:end]}
		if end < len(visible) {
			body["has_more"] = true
			body["response_metadata"] = map[string]any{"next_cursor": strconv.Itoa(end)}
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Errorf("encode conversations.replies response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	s := newSlackContractConnector(t, server.URL)
	return s.threadMessageLoader("C1", slackContractTS(contractRootSeq), slackContractTS(contractTriggerSeq), false)
}

func slackStuckCursorLoader(t *testing.T) func(context.Context, string) ([]Message, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.replies" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"has_more":true,"response_metadata":{"next_cursor":"stuck"},` +
			`"messages":[{"user":"U1","text":"looping page","ts":"171.002"}]}`))
	}))
	t.Cleanup(server.Close)

	s := newSlackContractConnector(t, server.URL)
	return s.threadMessageLoader("C1", slackContractTS(contractRootSeq), slackContractTS(contractTriggerSeq), false)
}

// --- 飞书 stub ------------------------------------------------------------

func feishuContractMessageID(seq int) string { return fmt.Sprintf("om_%d", seq) }

func feishuContractCreateTime(seq int) string { return strconv.Itoa(seq * 1000) }

type feishuStubSender struct {
	ID         string `json:"id"`
	SenderType string `json:"sender_type"`
	SenderName string `json:"sender_name"`
}

type feishuStubItem struct {
	MessageID  string           `json:"message_id"`
	RootID     string           `json:"root_id,omitempty"`
	ThreadID   string           `json:"thread_id"`
	MsgType    string           `json:"msg_type"`
	CreateTime string           `json:"create_time"`
	Sender     feishuStubSender `json:"sender"`
	Body       struct {
		Content string `json:"content"`
	} `json:"body"`
}

func feishuContractItem(t *testing.T, scripted contractScriptMessage) feishuStubItem {
	t.Helper()
	content, err := json.Marshal(map[string]string{"text": scripted.text})
	if err != nil {
		t.Fatalf("encode feishu text content: %v", err)
	}
	item := feishuStubItem{
		MessageID:  feishuContractMessageID(scripted.seq),
		ThreadID:   "omt_topic",
		MsgType:    "text",
		CreateTime: feishuContractCreateTime(scripted.seq),
	}
	item.Body.Content = string(content)
	if scripted.seq != contractRootSeq {
		item.RootID = feishuContractMessageID(contractRootSeq)
	}
	switch scripted.role {
	case contractSelfBot:
		item.Sender = feishuStubSender{ID: "daymug_app", SenderType: "app", SenderName: "DayMug"}
	case contractOtherBot:
		item.Sender = feishuStubSender{ID: "cli_alert", SenderType: "app", SenderName: "Helper"}
	default:
		item.Sender = feishuStubSender{ID: "ou_alice", SenderType: "user", SenderName: "Alice"}
	}
	return item
}

func feishuContractResponse(t *testing.T, data map[string]any) *http.Response {
	t.Helper()
	body, err := json.Marshal(map[string]any{"code": 0, "msg": "success", "data": data})
	if err != nil {
		t.Fatalf("encode feishu response: %v", err)
	}
	return feishuJSONResponse(string(body))
}

func newFeishuContractConnector(mockHTTP *feishuMockHTTP) *FeishuConnector {
	f := NewFeishuConnector(FeishuConfig{AppID: "daymug_app"})
	f.rest = lark.NewClient("daymug_app", "secret", lark.WithHttpClient(mockHTTP))
	return f
}

func feishuContractThreadLoader(f *FeishuConnector) func(context.Context, string) ([]Message, error) {
	return f.threadMessageLoader(
		"oc_1",
		"omt_topic",
		feishuContractMessageID(contractRootSeq),
		feishuContractMessageID(contractTriggerSeq),
		feishuContractCreateTime(contractTriggerSeq),
		false,
	)
}

func feishuContractLoader(t *testing.T, script []contractScriptMessage) func(context.Context, string) ([]Message, error) {
	t.Helper()
	items := make([]feishuStubItem, 0, len(script))
	for _, scripted := range script {
		items = append(items, feishuContractItem(t, scripted))
	}
	rootPath := "/open-apis/im/v1/messages/" + feishuContractMessageID(contractRootSeq)

	mockHTTP := &feishuMockHTTP{do: func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return feishuJSONResponse(`{"code":0,"msg":"success","tenant_access_token":"token","expire":7200}`), nil
		case rootPath:
			return feishuContractResponse(t, map[string]any{"items": items[:1]}), nil
		case "/open-apis/im/v1/messages":
			// Mirror the real endpoint's sort_type: the loader pages backwards
			// from the trigger so that a truncated backfill keeps the newest
			// context, and it reverses the result itself.
			paged := items
			if req.URL.Query().Get("sort_type") == "ByCreateTimeDesc" {
				paged = slices.Clone(items)
				slices.Reverse(paged)
			}
			offset := 0
			if token := req.URL.Query().Get("page_token"); token != "" {
				parsed, err := strconv.Atoi(token)
				if err != nil {
					t.Errorf("unexpected page_token %q", token)
					return nil, fmt.Errorf("bad page token %q", token)
				}
				offset = parsed
			}
			end := min(offset+contractPageSize, len(paged))
			data := map[string]any{"items": paged[offset:end], "has_more": end < len(paged)}
			if end < len(paged) {
				data["page_token"] = strconv.Itoa(end)
			}
			return feishuContractResponse(t, data), nil
		default:
			t.Errorf("unexpected Feishu request: %s", req.URL.String())
			return nil, fmt.Errorf("unexpected request %s", req.URL.Path)
		}
	}}

	return feishuContractThreadLoader(newFeishuContractConnector(mockHTTP))
}

func feishuStuckPageTokenLoader(t *testing.T) func(context.Context, string) ([]Message, error) {
	t.Helper()
	looping := feishuContractItem(t, contractThread[1])
	rootPath := "/open-apis/im/v1/messages/" + feishuContractMessageID(contractRootSeq)

	mockHTTP := &feishuMockHTTP{do: func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return feishuJSONResponse(`{"code":0,"msg":"success","tenant_access_token":"token","expire":7200}`), nil
		case rootPath:
			return feishuContractResponse(t, map[string]any{
				"items": []feishuStubItem{feishuContractItem(t, contractThread[0])},
			}), nil
		case "/open-apis/im/v1/messages":
			return feishuContractResponse(t, map[string]any{
				"items": []feishuStubItem{looping}, "has_more": true, "page_token": "stuck",
			}), nil
		default:
			t.Errorf("unexpected Feishu request: %s", req.URL.String())
			return nil, fmt.Errorf("unexpected request %s", req.URL.Path)
		}
	}}

	return feishuContractThreadLoader(newFeishuContractConnector(mockHTTP))
}
