package wechat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// newTestClient points a client at a stub gateway with a fixed token.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(ClientOptions{
		BaseURL:        srv.URL,
		Token:          "test-token",
		BotAgent:       "DayMug/9.9.9",
		ChannelVersion: "1.2.3",
	})
}

func TestRequestCarriesGatewayHeaders(t *testing.T) {
	var got http.Header
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"ret":0}`))
	})

	if _, err := client.GetUpdates(context.Background(), "", 0); err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}

	if want := "Bearer test-token"; got.Get("Authorization") != want {
		t.Errorf("Authorization = %q, want %q", got.Get("Authorization"), want)
	}
	if want := "ilink_bot_token"; got.Get("AuthorizationType") != want {
		t.Errorf("AuthorizationType = %q, want %q", got.Get("AuthorizationType"), want)
	}
	if want := "bot"; got.Get("iLink-App-Id") != want {
		t.Errorf("iLink-App-Id = %q, want %q", got.Get("iLink-App-Id"), want)
	}
	// 1.2.3 packs as 0x010203.
	if want := strconv.Itoa(0x010203); got.Get("iLink-App-ClientVersion") != want {
		t.Errorf("iLink-App-ClientVersion = %q, want %q", got.Get("iLink-App-ClientVersion"), want)
	}
}

// The gateway treats X-WECHAT-UIN as a replay guard, so it must decode to a
// decimal uint32 and differ between requests.
func TestWechatUinHeaderIsFreshDecimalUint32(t *testing.T) {
	seen := map[string]bool{}
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("X-WECHAT-UIN")
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			t.Errorf("X-WECHAT-UIN %q is not base64: %v", raw, err)
			return
		}
		if _, err := strconv.ParseUint(string(decoded), 10, 32); err != nil {
			t.Errorf("X-WECHAT-UIN decodes to %q, want a decimal uint32", decoded)
		}
		seen[raw] = true
		_, _ = w.Write([]byte(`{"ret":0}`))
	})

	for range 5 {
		if _, err := client.GetUpdates(context.Background(), "", 0); err != nil {
			t.Fatalf("GetUpdates: %v", err)
		}
	}
	if len(seen) < 2 {
		t.Errorf("got %d distinct X-WECHAT-UIN values across 5 requests, want them to vary", len(seen))
	}
}

func TestRequestCarriesBaseInfo(t *testing.T) {
	var body GetUpdatesRequest
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	})

	if _, err := client.GetUpdates(context.Background(), "cursor-1", 0); err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}

	if body.GetUpdatesBuf != "cursor-1" {
		t.Errorf("get_updates_buf = %q, want %q", body.GetUpdatesBuf, "cursor-1")
	}
	if body.BaseInfo == nil {
		t.Fatal("base_info missing from request")
	}
	if body.BaseInfo.BotAgent != "DayMug/9.9.9" {
		t.Errorf("bot_agent = %q, want %q", body.BaseInfo.BotAgent, "DayMug/9.9.9")
	}
	if body.BaseInfo.ChannelVersion != "1.2.3" {
		t.Errorf("channel_version = %q, want %q", body.BaseInfo.ChannelVersion, "1.2.3")
	}
}

func TestHTTPErrorIsReportedWithStatus(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`nope`))
	})

	_, err := client.GetUpdates(context.Background(), "", 0)
	if err == nil {
		t.Fatal("GetUpdates succeeded on HTTP 401")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not an *APIError", err)
	}
	if apiErr.Status != http.StatusUnauthorized {
		t.Errorf("Status = %d, want %d", apiErr.Status, http.StatusUnauthorized)
	}
}

func TestSanitizeBotAgent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty falls back", "", defaultBotAgent},
		{"whitespace only falls back", "   \n\t ", defaultBotAgent},
		{"plain value kept", "DayMug/1.2.3", "DayMug/1.2.3"},
		{"comment kept", "DayMug/1.2.3 (linux)", "DayMug/1.2.3 (linux)"},
		{"newlines cannot forge log structure", "DayMug/1.0\nInjected: yes", "DayMug/1.0 Injected: yes"},
		{"non-ascii dropped", "DayMug/1.0 中文", "DayMug/1.0"},
		{"runs of spaces collapse", "DayMug/1.0     x", "DayMug/1.0 x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeBotAgent(tc.in); got != tc.want {
				t.Errorf("SanitizeBotAgent(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeBotAgentCapsLength(t *testing.T) {
	got := SanitizeBotAgent(strings.Repeat("a", botAgentMaxLen+50))
	if len(got) > botAgentMaxLen {
		t.Errorf("length = %d, want <= %d", len(got), botAgentMaxLen)
	}
}

func TestEncodeClientVersion(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"1.0.11", 0x01000b},
		{"0.0.1", 0x000001},
		{"2.10.3", 0x020a03},
		{"1.0.0-dev", 0x010000}, // a suffixed patch degrades rather than failing
		{"garbage", 0},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := encodeClientVersion(tc.in); got != strconv.Itoa(tc.want) {
				t.Errorf("encodeClientVersion(%q) = %s, want %d", tc.in, got, tc.want)
			}
		})
	}
}
