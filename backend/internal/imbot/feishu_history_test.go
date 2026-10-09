package imbot

// Bounds on the 飞书 thread backfill. Unlike Slack there is no per-thread
// listing endpoint, so the loader walks the whole chat; these tests pin the
// page/item ceilings, the standalone deadline and the bounded time window that
// keep a busy channel from turning one mention into thousands of API calls.

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/DayMug/DayMug/backend/internal/prompts"
)

// feishuHistoryItem renders one scripted chat message. seq drives both the id
// and the create time so ordering assertions stay readable.
func feishuHistoryItem(seq int) string {
	return `{"message_id":"om_` + strconv.Itoa(seq) + `","root_id":"om_root","thread_id":"omt_topic",` +
		`"msg_type":"text","create_time":"` + strconv.Itoa(seq*1000) + `",` +
		`"sender":{"id":"ou_alice","sender_type":"user","sender_name":"Alice"},` +
		`"body":{"content":"{\"text\":\"m` + strconv.Itoa(seq) + `\"}"}}`
}

const feishuHistoryRootItem = `{"message_id":"om_root","thread_id":"omt_topic","msg_type":"text","create_time":"1000",` +
	`"sender":{"id":"ou_alice","sender_type":"user","sender_name":"Alice"},"body":{"content":"{\"text\":\"root\"}"}}`

// feishuHistoryPage encodes one im/v1/messages page. seqs are emitted in the
// given order, which the stubs keep newest-first to match ByCreateTimeDesc.
func feishuHistoryPage(seqs []int, nextToken string) string {
	items := make([]string, 0, len(seqs))
	for _, seq := range seqs {
		items = append(items, feishuHistoryItem(seq))
	}
	page := `{"code":0,"msg":"success","data":{"items":[` + strings.Join(items, ",") + `]`
	if nextToken != "" {
		page += `,"has_more":true,"page_token":"` + nextToken + `"`
	} else {
		page += `,"has_more":false`
	}
	return page + `}}`
}

// feishuHistoryConnector wires a connector to a stub that serves the thread
// root plus whatever list pages the caller's handler produces, and records the
// query of every list request.
func feishuHistoryConnector(t *testing.T, list func() string) (*FeishuConnector, *[]url.Values) {
	t.Helper()
	queries := &[]url.Values{}
	mockHTTP := &feishuMockHTTP{do: func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			return feishuJSONResponse(`{"code":0,"msg":"success","tenant_access_token":"token","expire":7200}`), nil
		case "/open-apis/im/v1/messages/om_root":
			return feishuJSONResponse(`{"code":0,"msg":"success","data":{"items":[` + feishuHistoryRootItem + `]}}`), nil
		case "/open-apis/im/v1/messages":
			// The backfill must never inherit the caller's unbounded ctx.
			if _, ok := req.Context().Deadline(); !ok {
				t.Error("thread backfill ran on a deadline-less context")
			}
			*queries = append(*queries, req.URL.Query())
			return feishuJSONResponse(list()), nil
		default:
			t.Errorf("unexpected Feishu request: %s", req.URL.String())
			return nil, nil
		}
	}}
	f := NewFeishuConnector(FeishuConfig{AppID: "daymug_app"})
	f.rest = lark.NewClient("daymug_app", "secret", lark.WithHttpClient(mockHTTP))
	return f, queries
}

func feishuHistoryTexts(history []Message) []string {
	texts := make([]string, 0, len(history))
	for _, msg := range history {
		texts = append(texts, msg.Text)
	}
	return texts
}

func TestFeishuThreadHistoryStopsAtMaxPages(t *testing.T) {
	// Every page claims more data behind a fresh token, so only the page cap
	// can end the walk.
	call := 0
	f, queries := feishuHistoryConnector(t, func() string {
		call++
		return feishuHistoryPage([]int{100 - call}, "t"+strconv.Itoa(call))
	})

	loader := f.threadMessageLoader("oc_1", "omt_topic", "om_root", "om_trigger", "500000", false)
	history, err := loader(context.Background(), "")
	if err != nil {
		t.Fatalf("load thread history: %v", err)
	}
	if len(*queries) != feishuHistoryMaxPages {
		t.Fatalf("issued %d list calls, want the %d-page cap", len(*queries), feishuHistoryMaxPages)
	}
	if len(history) == 0 || history[0].Text != prompts.FeishuHistoryTruncated {
		t.Fatalf("history did not lead with the truncation notice: %q", feishuHistoryTexts(history))
	}
	// Everything already fetched is still handed over: notice, root, pages.
	if len(history) != feishuHistoryMaxPages+2 {
		t.Fatalf("history = %q, want notice + root + %d paged messages", feishuHistoryTexts(history), feishuHistoryMaxPages)
	}
	if history[1].MessageID != "om_root" || history[2].Text != "m80" || history[len(history)-1].Text != "m99" {
		t.Fatalf("history is not root-then-oldest-first: %q", feishuHistoryTexts(history))
	}
}

func TestFeishuThreadHistoryStopsAtMaxItems(t *testing.T) {
	// The API is not contractually bound to honour page_size, so an oversized
	// page must be stopped by the item cap well before the page cap.
	const oversized = 400
	next := 1300
	f, queries := feishuHistoryConnector(t, func() string {
		seqs := make([]int, 0, oversized)
		for i := 0; i < oversized; i++ {
			next--
			seqs = append(seqs, next)
		}
		return feishuHistoryPage(seqs, "t"+strconv.Itoa(next))
	})

	loader := f.threadMessageLoader("oc_1", "omt_topic", "om_root", "om_trigger", "5000000", false)
	history, err := loader(context.Background(), "")
	if err != nil {
		t.Fatalf("load thread history: %v", err)
	}
	wantCalls := feishuHistoryMaxItems/oversized + 1
	if len(*queries) != wantCalls {
		t.Fatalf("issued %d list calls, want %d before the %d-item cap", len(*queries), wantCalls, feishuHistoryMaxItems)
	}
	if len(*queries) >= feishuHistoryMaxPages {
		t.Fatalf("item cap did not bind before the page cap (%d calls)", len(*queries))
	}
	if len(history) == 0 || history[0].Text != prompts.FeishuHistoryTruncated {
		t.Fatalf("history did not lead with the truncation notice: %q", history[0].Text)
	}
	// notice + root + exactly the newest feishuHistoryMaxItems chat messages.
	if len(history) != feishuHistoryMaxItems+2 {
		t.Fatalf("history kept %d entries, want notice + root + %d", len(history), feishuHistoryMaxItems)
	}
	if history[len(history)-1].Text != "m1299" || history[2].Text != "m300" {
		t.Fatalf("truncation dropped the newest messages instead of the oldest: %q..%q",
			history[2].Text, history[len(history)-1].Text)
	}
}

func TestFeishuThreadHistoryWithoutRootUsesBoundedWindow(t *testing.T) {
	// A topic-group reply can arrive with an empty root_id. Without a root
	// timestamp the listing must still be floored, or it pages back to the
	// channel's very first message ever.
	const triggerMillis = "1700000000000"
	f, queries := feishuHistoryConnector(t, func() string {
		return feishuHistoryPage([]int{2}, "")
	})

	loader := f.threadMessageLoader("oc_1", "omt_topic", "", "om_trigger", triggerMillis, false)
	if loader == nil {
		t.Fatal("connector exposed no thread-history loader")
	}
	if _, err := loader(context.Background(), ""); err != nil {
		t.Fatalf("load thread history: %v", err)
	}
	if len(*queries) != 1 {
		t.Fatalf("issued %d list calls, want 1", len(*queries))
	}
	start := (*queries)[0].Get("start_time")
	wantStart := strconv.FormatInt(1700000000-int64(feishuHistoryLookback/time.Second), 10)
	if start != wantStart {
		t.Fatalf("start_time = %q, want the bounded lookback %q", start, wantStart)
	}
	if end := (*queries)[0].Get("end_time"); end != "1700000000" {
		t.Fatalf("end_time = %q, want the trigger timestamp", end)
	}
}

func TestFeishuThreadHistoryLeavesSmallThreadsIntact(t *testing.T) {
	// Guard against over-fixing: an ordinary thread pages once, carries no
	// notice, and keeps every message.
	f, queries := feishuHistoryConnector(t, func() string {
		return feishuHistoryPage([]int{3, 2}, "")
	})

	loader := f.threadMessageLoader("oc_1", "omt_topic", "om_root", "om_trigger", "500000", false)
	history, err := loader(context.Background(), "")
	if err != nil {
		t.Fatalf("load thread history: %v", err)
	}
	if len(*queries) != 1 {
		t.Fatalf("issued %d list calls, want 1", len(*queries))
	}
	got := strings.Join(feishuHistoryTexts(history), "|")
	if got != "root|m2|m3" {
		t.Fatalf("history = %q, want %q with no truncation notice", got, "root|m2|m3")
	}
}

func TestFeishuHistoryStartPrefersRootThenLookback(t *testing.T) {
	rooted := feishuHistoryStart(&larkim.Message{CreateTime: sp("1000")}, "1700000000000")
	if rooted != "1" {
		t.Fatalf("start with root = %q, want the root's second-precision create time", rooted)
	}
	fallback := feishuHistoryStart(nil, "1700000000000")
	if want := strconv.FormatInt(1700000000-int64(feishuHistoryLookback/time.Second), 10); fallback != want {
		t.Fatalf("start without root = %q, want the bounded lookback %q", fallback, want)
	}
	if floor := feishuHistoryStart(nil, "1000"); floor != "1" {
		t.Fatalf("start = %q, want a clamp to a positive timestamp", floor)
	}
}
