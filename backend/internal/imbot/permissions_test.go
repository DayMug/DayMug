package imbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/slack-go/slack"
)

func TestSlackConnectionChecksIdentityAppTokenAndEveryScope(t *testing.T) {
	grantedScopes := []string{
		"app_mentions:read", "chat:write", "channels:history", "groups:history",
		"im:history", "users:read", "files:read", "files:write",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth.test":
			w.Header().Set("X-OAuth-Scopes", strings.Join(grantedScopes, ","))
			_, _ = w.Write([]byte(`{"ok":true,"team":"Acme","user":"DayMug","team_id":"T1","user_id":"U1","bot_id":"B1"}`))
		case "/apps.connections.open":
			if got := r.Header.Get("Authorization"); got != "Bearer xapp-test" {
				t.Fatalf("Authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"ok":true,"url":"wss://example.invalid/socket"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := testSlackConnection(context.Background(), BotConfig{
		Platform: PlatformSlack, BotToken: "xoxb-test", AppToken: "xapp-test",
	}, []slack.Option{slack.OptionAPIURL(server.URL + "/"), slack.OptionHTTPClient(server.Client())})

	if !result.Connected || !result.AllRequiredPermissionsGranted {
		t.Fatalf("result = %+v", result)
	}
	for _, permission := range result.Permissions {
		if !permission.Granted {
			t.Errorf("permission %q was not granted", permission.Key)
		}
	}
}

func TestSlackConnectionReportsMissingScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth.test":
			w.Header().Set("X-OAuth-Scopes", "chat:write")
			_, _ = w.Write([]byte(`{"ok":true,"team":"Acme","user":"DayMug","bot_id":"B1"}`))
		case "/apps.connections.open":
			_, _ = w.Write([]byte(`{"ok":true,"url":"wss://example.invalid/socket"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := testSlackConnection(context.Background(), BotConfig{
		Platform: PlatformSlack, BotToken: "xoxb-test", AppToken: "xapp-test",
	}, []slack.Option{slack.OptionAPIURL(server.URL + "/"), slack.OptionHTTPClient(server.Client())})

	if !result.Connected {
		t.Fatalf("credentials should connect: %+v", result)
	}
	if result.AllRequiredPermissionsGranted {
		t.Fatalf("missing permissions reported as complete: %+v", result)
	}
	if permissionGranted(result.Permissions, "channels:history") {
		t.Fatal("channels:history unexpectedly granted")
	}
}

func TestFeishuConnectionChecksBotCapabilityAndGrantedScopes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"t-test","expire":7200}`))
		case "/open-apis/bot/v3/info":
			_, _ = w.Write([]byte(`{"code":0,"bot":{"open_id":"ou_bot"}}`))
		case "/open-apis/application/v6/scopes":
			scopes := []map[string]any{
				{"scope_name": "im:message", "grant_status": 1, "scope_type": "tenant"},
				{"scope_name": "im:message:send_as_bot", "grant_status": 1, "scope_type": "tenant"},
				{"scope_name": "im:resource", "grant_status": 1, "scope_type": "tenant"},
				{"scope_name": "im:message.group_msg", "grant_status": 1, "scope_type": "tenant"},
				{"scope_name": "contact:user.base:readonly", "grant_status": 0, "scope_type": "tenant"},
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"scopes": scopes}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := testFeishuConnection(context.Background(), BotConfig{
		Platform: PlatformFeishu, AppID: "cli_test", AppSecret: "secret",
	}, []lark.ClientOptionFunc{
		lark.WithOpenBaseUrl(server.URL),
		lark.WithHttpClient(server.Client()),
	})

	if !result.Connected || !result.AllRequiredPermissionsGranted {
		t.Fatalf("result = %+v", result)
	}
	if result.Identity != "ou_bot" {
		t.Fatalf("identity = %q", result.Identity)
	}
	if permissionGranted(result.Permissions, "contact:user.base:readonly") {
		t.Fatal("optional contact permission unexpectedly granted")
	}
	if !permissionGranted(result.Permissions, "im:message:recall") {
		t.Fatal("message recall capability should be covered by im:message")
	}
}

func permissionGranted(checks []PermissionCheck, key string) bool {
	for _, check := range checks {
		if check.Key == key {
			return check.Granted
		}
	}
	return false
}

// Regression: the probe used to call getConfig, which requires an
// ilink_user_id that does not exist before anyone has messaged the bot. A
// perfectly good pairing therefore failed with "ilink_user_id required".
func TestWeChatConnectionProbesWithoutAUserID(t *testing.T) {
	var hit []string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = append(hit, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "getconfig") {
			// What the real gateway answers with a blank user id.
			_, _ = w.Write([]byte(`{"ret":-2,"errmsg":"ilink_user_id required"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer gateway.Close()

	got := testWeChatConnection(context.Background(),
		BotConfig{Platform: PlatformWeChat, BotToken: "tok", AppID: gateway.URL}, gateway.URL)

	if !got.Connected {
		t.Fatalf("Connected = false, error = %q", got.Error)
	}
	if !got.AllRequiredPermissionsGranted {
		t.Errorf("permissions = %+v, want every required one granted", got.Permissions)
	}
	for _, path := range hit {
		if strings.HasSuffix(path, "getconfig") {
			t.Error("the probe called getConfig, which needs a user id it cannot have")
		}
	}
}

func TestWeChatConnectionReportsAnExpiredPairing(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ret":-14,"errmsg":"session timeout"}`))
	}))
	defer gateway.Close()

	got := testWeChatConnection(context.Background(),
		BotConfig{Platform: PlatformWeChat, BotToken: "stale", AppID: gateway.URL}, gateway.URL)

	if got.Connected {
		t.Fatal("Connected = true despite a rejected token")
	}
	if !strings.Contains(got.Error, "session timeout") {
		t.Errorf("Error = %q, want the gateway's own message", got.Error)
	}
}

// Without both halves there is nothing to probe, so the test must say
// "not paired" rather than emit a request that cannot succeed.
func TestWeChatConnectionRequiresBothHalvesOfThePairing(t *testing.T) {
	called := false
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer gateway.Close()

	got := testWeChatConnection(context.Background(),
		BotConfig{Platform: PlatformWeChat, BotToken: "tok"}, "")

	if got.Connected {
		t.Error("Connected = true without a paired gateway")
	}
	if !strings.Contains(got.Error, "not paired") {
		t.Errorf("Error = %q, want it to point at pairing", got.Error)
	}
	if called {
		t.Error("a request was sent despite the missing gateway")
	}
}
