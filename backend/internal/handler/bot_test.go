package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func setupAgentBotRouter(ms *storetest.Fake, testers ...func(context.Context, imbot.BotConfig) imbot.ConnectionTestResult) *gin.Engine {
	r := gin.New()
	h := &BotHandler{Store: ms, Bots: ms}
	if len(testers) > 0 {
		h.TestConnection = testers[0]
	}
	r.GET("/api/users/:id/bots", h.List)
	r.GET("/api/users/:id/bots/requirements", h.Requirements)
	r.POST("/api/users/:id/bots/test-connection", h.Test)
	r.POST("/api/users/:id/bots", h.Create)
	r.PUT("/api/users/:id/bots/:botId", h.Update)
	r.DELETE("/api/users/:id/bots/:botId", h.Delete)
	return r
}

func TestBotHandlerCreatesMultipleBotsForAgent(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "agent", OwnerID: "owner", Name: "Agent"}}
	r := setupAgentBotRouter(ms)
	for _, body := range []string{
		`{"name":"Slack","platform":"slack","enabled":false,"model":"claude-sonnet","channels":"[{\"channel\":\"*\"}]"}`,
		`{"name":"Feishu","platform":"feishu","enabled":false,"channels":"[{\"channel\":\"dm\"}]"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/users/agent/bots", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create status %d: %s", rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/users/agent/bots", http.NoBody))
	var bots []store.Bot
	if err := json.NewDecoder(rec.Body).Decode(&bots); err != nil {
		t.Fatal(err)
	}
	if len(bots) != 2 || bots[0].AgentID != "agent" ||
		bots[0].Model != "claude-sonnet" || bots[1].AgentID != "agent" {
		t.Fatalf("bots = %+v", bots)
	}
}

// botSentinels are values no response body may ever contain. They are shaped
// like the real thing so a partial-masking regression would still be caught.
const (
	sentinelBotToken  = "xoxb-SENTINEL-bot-token"
	sentinelAppToken  = "xapp-SENTINEL-app-token"
	sentinelAppID     = "cli_SENTINEL-app-id"
	sentinelAppSecret = "SENTINEL-app-secret"
)

// createBot posts body and returns the recorded response.
func createBot(t *testing.T, r *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/users/agent/bots", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body.String())
	}
	return rec
}

func updateBot(t *testing.T, r *gin.Engine, botID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/users/agent/bots/"+botID, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status %d: %s", rec.Code, rec.Body.String())
	}
	return rec
}

func TestBotHandlerNeverReturnsCredentials(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "agent", OwnerID: "owner", Name: "Agent"}}
	r := setupAgentBotRouter(ms)

	created := createBot(t, r, `{"name":"Slack","platform":"slack","enabled":true,`+
		`"bot_token":"`+sentinelBotToken+`","bot_app_token":"`+sentinelAppToken+`",`+
		`"channels":"[{\"channel\":\"*\"}]"}`)
	createdFeishu := createBot(t, r, `{"name":"Feishu","platform":"feishu","enabled":true,`+
		`"bot_app_id":"`+sentinelAppID+`","bot_app_secret":"`+sentinelAppSecret+`",`+
		`"channels":"[{\"channel\":\"dm\"}]"}`)

	var slack botResponse
	if err := json.Unmarshal(created.Body.Bytes(), &slack); err != nil {
		t.Fatal(err)
	}
	updated := updateBot(t, r, slack.ID, `{"name":"Slack","platform":"slack","enabled":true,`+
		`"channels":"[{\"channel\":\"dm\"}]"}`)

	listRec := httptest.NewRecorder()
	r.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/api/users/agent/bots", http.NoBody))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", listRec.Code, listRec.Body.String())
	}

	for name, body := range map[string]string{
		"create slack":  created.Body.String(),
		"create feishu": createdFeishu.Body.String(),
		"update":        updated.Body.String(),
		"list":          listRec.Body.String(),
	} {
		for _, secret := range []string{sentinelBotToken, sentinelAppToken, sentinelAppID, sentinelAppSecret} {
			if strings.Contains(body, secret) {
				t.Fatalf("%s response leaked %q: %s", name, secret, body)
			}
		}
	}

	if !slack.BotTokenConfigured || !slack.BotAppTokenConfigured || !slack.CredentialsConfigured {
		t.Fatalf("slack flags = %+v", slack)
	}
	if slack.BotAppIDConfigured || slack.BotAppSecretConfigured {
		t.Fatalf("slack bot reported feishu credentials: %+v", slack)
	}

	var feishu botResponse
	if err := json.Unmarshal(createdFeishu.Body.Bytes(), &feishu); err != nil {
		t.Fatal(err)
	}
	if !feishu.BotAppIDConfigured || !feishu.BotAppSecretConfigured || !feishu.CredentialsConfigured {
		t.Fatalf("feishu flags = %+v", feishu)
	}
}

func TestBotHandlerReportsMissingCredentials(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "agent", OwnerID: "owner", Name: "Agent"}}
	r := setupAgentBotRouter(ms)

	rec := createBot(t, r, `{"name":"Slack","platform":"slack","enabled":false,`+
		`"bot_token":"`+sentinelBotToken+`","channels":"[{\"channel\":\"*\"}]"}`)
	var resp botResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.BotTokenConfigured || resp.BotAppTokenConfigured || resp.CredentialsConfigured {
		t.Fatalf("half-configured slack bot flags = %+v", resp)
	}
}

func TestBotHandlerRoundTripsConfiguredReplies(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "agent", OwnerID: "owner", Name: "Agent"}}
	r := setupAgentBotRouter(ms)

	rec := createBot(t, r, `{"name":"Slack","platform":"slack","enabled":false,`+
		`"channels":"[]","max_conversation_duration":"3h","unconfigured_reply":"custom unconfigured",`+
		`"unauthorized_reply":"custom unauthorized"}`)
	var resp botResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.MaxConversationDuration != "3h" ||
		resp.UnconfiguredReply != "custom unconfigured" || resp.UnauthorizedReply != "custom unauthorized" {
		t.Fatalf("response = %+v", resp)
	}
	stored, err := ms.GetBot(context.Background(), resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MaxConversationDuration != resp.MaxConversationDuration ||
		stored.UnconfiguredReply != resp.UnconfiguredReply || stored.UnauthorizedReply != resp.UnauthorizedReply {
		t.Fatalf("stored bot = %+v", stored)
	}
}

// A channel-rules-only edit submits blank credential inputs, because the UI
// never received the stored secrets. Both enabled states matter: an enabled bot
// would fail validation on a wipe, but a disabled one would be silently
// emptied, so only the disabled case proves the merge itself.
func TestBotHandlerUpdateKeepsStoredCredentialsWhenBlank(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled string
	}{
		{"enabled bot", "true"},
		{"disabled bot", "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := storetest.New()
			ms.Users = []store.User{{ID: "agent", OwnerID: "owner", Name: "Agent"}}
			r := setupAgentBotRouter(ms)

			rec := createBot(t, r, `{"name":"Slack","platform":"slack","enabled":`+tc.enabled+`,`+
				`"bot_token":"`+sentinelBotToken+`","bot_app_token":"`+sentinelAppToken+`",`+
				`"channels":"[{\"channel\":\"*\"}]"}`)
			var created botResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}

			// Whitespace counts as blank too.
			resp := updateBot(t, r, created.ID, `{"name":"Slack","platform":"slack","enabled":`+tc.enabled+`,`+
				`"bot_token":"","bot_app_token":"   ","channels":"[{\"channel\":\"dm\"}]"}`)

			stored, err := ms.GetBot(context.Background(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.BotToken != sentinelBotToken || stored.BotAppToken != sentinelAppToken {
				t.Fatalf("blank update wiped credentials: %+v", stored)
			}
			if !strings.Contains(stored.Channels, `"dm"`) {
				t.Fatalf("channel rules were not updated: %+v", stored)
			}

			var updated botResponse
			if err := json.Unmarshal(resp.Body.Bytes(), &updated); err != nil {
				t.Fatal(err)
			}
			if !updated.BotTokenConfigured || !updated.BotAppTokenConfigured {
				t.Fatalf("update response claims credentials are gone: %+v", updated)
			}
		})
	}
}

func TestBotHandlerUpdateReplacesSuppliedCredentials(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "agent", OwnerID: "owner", Name: "Agent"}}
	r := setupAgentBotRouter(ms)

	rec := createBot(t, r, `{"name":"Slack","platform":"slack","enabled":true,`+
		`"bot_token":"`+sentinelBotToken+`","bot_app_token":"`+sentinelAppToken+`",`+
		`"channels":"[{\"channel\":\"*\"}]"}`)
	var created botResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	updateBot(t, r, created.ID, `{"name":"Slack","platform":"slack","enabled":true,`+
		`"bot_token":"xoxb-rotated","channels":"[{\"channel\":\"*\"}]"}`)

	stored, err := ms.GetBot(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BotToken != "xoxb-rotated" {
		t.Fatalf("supplied bot_token was not stored: %+v", stored)
	}
	if stored.BotAppToken != sentinelAppToken {
		t.Fatalf("untouched app token changed: %+v", stored)
	}
}

func TestBotHandlerTestsSavedBotWithStoredCredentials(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "agent", OwnerID: "owner", Name: "Agent"}}
	var seen imbot.BotConfig
	r := setupAgentBotRouter(ms, func(_ context.Context, cfg imbot.BotConfig) imbot.ConnectionTestResult {
		seen = cfg
		return imbot.ConnectionTestResult{Platform: imbot.PlatformSlack, Connected: true}
	})

	rec := createBot(t, r, `{"name":"Slack","platform":"slack","enabled":true,`+
		`"bot_token":"`+sentinelBotToken+`","bot_app_token":"`+sentinelAppToken+`",`+
		`"channels":"[{\"channel\":\"*\"}]"}`)
	var created botResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	body := `{"bot_id":"` + created.ID + `","name":"Slack","platform":"slack"}`
	req := httptest.NewRequest(http.MethodPost, "/api/users/agent/bots/test-connection", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	testRec := httptest.NewRecorder()
	r.ServeHTTP(testRec, req)

	if testRec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", testRec.Code, testRec.Body.String())
	}
	if seen.BotToken != sentinelBotToken || seen.AppToken != sentinelAppToken {
		t.Fatalf("stored credentials were not used: %+v", seen)
	}
}

func TestBotHandlerRejectsCrossAgentRule(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "agent", OwnerID: "owner", Name: "Agent"}}
	r := setupAgentBotRouter(ms)
	body := `{"name":"Slack","platform":"slack","channels":"[{\"channel\":\"C1\",\"agent_id\":\"other\"}]"}`
	req := httptest.NewRequest(http.MethodPost, "/api/users/agent/bots", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestBotHandlerReturnsPermissionRequirements(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "agent", OwnerID: "owner", Name: "Agent"}}
	r := setupAgentBotRouter(ms)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/users/agent/bots/requirements", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var requirements map[string][]imbot.PermissionRequirement
	if err := json.NewDecoder(rec.Body).Decode(&requirements); err != nil {
		t.Fatal(err)
	}
	if len(requirements[imbot.PlatformSlack]) == 0 || len(requirements[imbot.PlatformFeishu]) == 0 {
		t.Fatalf("requirements = %+v", requirements)
	}
}

func TestBotHandlerTestsUnsavedCredentialsWithoutPersisting(t *testing.T) {
	ms := storetest.New()
	ms.Users = []store.User{{ID: "agent", OwnerID: "owner", Name: "Agent"}}
	called := false
	r := setupAgentBotRouter(ms, func(_ context.Context, cfg imbot.BotConfig) imbot.ConnectionTestResult {
		called = true
		if cfg.Platform != imbot.PlatformSlack || cfg.BotToken != "xoxb-test" || cfg.AppToken != "xapp-test" {
			t.Fatalf("config = %+v", cfg)
		}
		return imbot.ConnectionTestResult{
			Platform: imbot.PlatformSlack, Connected: true, AllRequiredPermissionsGranted: true,
		}
	})

	body := `{"name":"Slack","platform":"slack","bot_token":"xoxb-test","bot_app_token":"xapp-test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/users/agent/bots/test-connection", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatal("connection tester was not called")
	}
	bots, err := ms.ListBots(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(bots) != 0 {
		t.Fatalf("test connection persisted bots: %+v", bots)
	}
}

// credentials_configured tells the admin UI whether a bot could be enabled
// as-is, so it must follow each platform's own credential set — a blank
// whitespace value counts as missing.
func TestBotResponseCredentialsConfiguredPerPlatform(t *testing.T) {
	tests := []struct {
		name string
		bot  store.Bot
		want bool
	}{
		{"slack complete", store.Bot{Platform: imbot.PlatformSlack, BotToken: "x", BotAppToken: "y"}, true},
		{"slack without app token", store.Bot{Platform: imbot.PlatformSlack, BotToken: "x", BotAppID: "i", BotAppSecret: "s"}, false},
		{"feishu complete", store.Bot{Platform: imbot.PlatformFeishu, BotAppID: "i", BotAppSecret: "s"}, true},
		{"feishu with a blank secret", store.Bot{Platform: imbot.PlatformFeishu, BotAppID: "i", BotAppSecret: "  "}, false},
		{"telegram needs only the bot token", store.Bot{Platform: imbot.PlatformTelegram, BotToken: "x"}, true},
		{"telegram without it", store.Bot{Platform: imbot.PlatformTelegram, BotAppToken: "y", BotAppID: "i", BotAppSecret: "s"}, false},
		{"wechat paired", store.Bot{Platform: imbot.PlatformWeChat, BotToken: "x", BotAppID: "gw"}, true},
		{"wechat without a gateway", store.Bot{Platform: imbot.PlatformWeChat, BotToken: "x"}, false},
		{"unknown platform", store.Bot{Platform: "irc", BotToken: "x", BotAppToken: "y", BotAppID: "i", BotAppSecret: "s"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := newBotResponse(tt.bot).CredentialsConfigured; got != tt.want {
				t.Fatalf("CredentialsConfigured = %v, want %v", got, tt.want)
			}
		})
	}
}
