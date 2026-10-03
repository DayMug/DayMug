package imbot

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/imbot/wechat"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkapplication "github.com/larksuite/oapi-sdk-go/v3/service/application/v6"
	"github.com/slack-go/slack"
)

const (
	PermissionCredentialBotToken = "bot_token"
	PermissionCredentialAppToken = "app_token"
	PermissionCredentialApp      = "app"
)

// PermissionRequirement describes one platform permission DayMug uses.
// Required=false marks an enhancement that is tested and displayed but does
// not prevent the core connection from being considered ready.
type PermissionRequirement struct {
	Key        string `json:"key"`
	Credential string `json:"credential"`
	Required   bool   `json:"required"`
}

type PermissionCheck struct {
	PermissionRequirement
	Granted bool `json:"granted"`
}

type ConnectionTestResult struct {
	Platform                      string            `json:"platform"`
	Connected                     bool              `json:"connected"`
	Identity                      string            `json:"identity,omitempty"`
	AllRequiredPermissionsGranted bool              `json:"all_required_permissions_granted"`
	Permissions                   []PermissionCheck `json:"permissions"`
	Error                         string            `json:"error,omitempty"`
}

// permissionRequirements is the one per-platform table kept outside the
// platforms registry: every connection test reads it through
// newPermissionChecks, and the registry holds those tests, so folding it in
// would be an initialization cycle. TestEveryRegisteredPlatformIsFullyDescribed
// keeps the two key sets identical instead.
var permissionRequirements = map[string][]PermissionRequirement{
	PlatformSlack: {
		{Key: "connections:write", Credential: PermissionCredentialAppToken, Required: true},
		{Key: "app_mentions:read", Credential: PermissionCredentialBotToken, Required: true},
		{Key: "chat:write", Credential: PermissionCredentialBotToken, Required: true},
		{Key: "chat:delete", Credential: PermissionCredentialBotToken, Required: true},
		{Key: "channels:history", Credential: PermissionCredentialBotToken, Required: true},
		{Key: "groups:history", Credential: PermissionCredentialBotToken, Required: true},
		{Key: "im:history", Credential: PermissionCredentialBotToken, Required: true},
		{Key: "users:read", Credential: PermissionCredentialBotToken, Required: true},
		{Key: "files:read", Credential: PermissionCredentialBotToken, Required: false},
		{Key: "files:write", Credential: PermissionCredentialBotToken, Required: false},
	},
	// Telegram has no scope system: one token grants every Bot API method. The
	// checks that matter are therefore the @BotFather switches and the
	// single-consumer rule for getUpdates, all readable over the API.
	PlatformTelegram: {
		{Key: "bot_token", Credential: PermissionCredentialBotToken, Required: true},
		{Key: "polling_available", Credential: PermissionCredentialBotToken, Required: true},
		{Key: "can_join_groups", Credential: PermissionCredentialBotToken, Required: false},
		{Key: "can_read_all_group_messages", Credential: PermissionCredentialBotToken, Required: false},
	},
	// Weixin has no scope system either, and a bot token is issued by a phone
	// confirmation rather than granted per capability. What can be checked is
	// that the pairing is complete and the gateway still honours the token.
	PlatformWeChat: {
		{Key: "paired", Credential: PermissionCredentialBotToken, Required: true},
		{Key: "token_accepted", Credential: PermissionCredentialBotToken, Required: true},
	},
	PlatformFeishu: {
		{Key: "im:message", Credential: PermissionCredentialApp, Required: true},
		{Key: "im:message:send_as_bot", Credential: PermissionCredentialApp, Required: true},
		{Key: "im:message:recall", Credential: PermissionCredentialApp, Required: true},
		{Key: "im:resource", Credential: PermissionCredentialApp, Required: true},
		{Key: "im:message.group_msg", Credential: PermissionCredentialApp, Required: true},
		{Key: "contact:user.base:readonly", Credential: PermissionCredentialApp, Required: false},
	},
}

func PermissionRequirements() map[string][]PermissionRequirement {
	result := make(map[string][]PermissionRequirement, len(permissionRequirements))
	for platform, requirements := range permissionRequirements {
		result[platform] = append([]PermissionRequirement(nil), requirements...)
	}
	return result
}

func TestConnection(ctx context.Context, cfg BotConfig) ConnectionTestResult {
	d, ok := platforms[cfg.Platform]
	if !ok {
		return ConnectionTestResult{Platform: cfg.Platform, Error: fmt.Sprintf("unknown platform %q", cfg.Platform)}
	}
	return d.testConnection(ctx, cfg)
}

func newPermissionChecks(platform string) []PermissionCheck {
	requirements := permissionRequirements[platform]
	checks := make([]PermissionCheck, len(requirements))
	for i, requirement := range requirements {
		checks[i].PermissionRequirement = requirement
	}
	return checks
}

func finishConnectionTest(result ConnectionTestResult) ConnectionTestResult {
	result.AllRequiredPermissionsGranted = result.Connected
	for _, permission := range result.Permissions {
		if permission.Required && !permission.Granted {
			result.AllRequiredPermissionsGranted = false
			break
		}
	}
	return result
}

func testSlackConnection(ctx context.Context, cfg BotConfig, options []slack.Option) ConnectionTestResult {
	result := ConnectionTestResult{
		Platform:    PlatformSlack,
		Permissions: newPermissionChecks(PlatformSlack),
	}
	if strings.TrimSpace(cfg.BotToken) == "" || strings.TrimSpace(cfg.AppToken) == "" {
		result.Error = "bot_token and app_token are required"
		return finishConnectionTest(result)
	}

	var responseHeaders http.Header
	clientOptions := append([]slack.Option{}, options...)
	clientOptions = append(clientOptions,
		slack.OptionAppLevelToken(cfg.AppToken),
		slack.OptionOnResponseHeaders(func(path string, headers http.Header) {
			if path == "auth.test" {
				responseHeaders = headers.Clone()
			}
		}),
	)
	api := slack.New(cfg.BotToken, clientOptions...)

	var failures []string
	auth, err := api.AuthTestContext(ctx)
	identityErr := validateSlackBotIdentity(auth)
	switch {
	case err != nil:
		failures = append(failures, fmt.Sprintf("bot token: %v", err))
	case identityErr != nil:
		failures = append(failures, identityErr.Error())
	default:
		result.Identity = strings.TrimSpace(auth.Team + " / " + auth.User)
		granted := splitScopes(responseHeaders.Get("X-OAuth-Scopes"))
		for i := range result.Permissions {
			if result.Permissions[i].Credential == PermissionCredentialBotToken {
				key := result.Permissions[i].Key
				// Slack exposes no distinct delete scope: chat.delete uses the
				// same chat:write bot scope and can remove messages posted by
				// that bot. Keep a separate capability row in the admin check so
				// operators can see that final-message cleanup is covered.
				if key == "chat:delete" {
					key = "chat:write"
				}
				result.Permissions[i].Granted = granted[key]
			}
		}
	}

	_, socketURL, err := api.StartSocketModeContext(ctx)
	switch {
	case err != nil:
		failures = append(failures, fmt.Sprintf("app-level token: %v", err))
	case strings.TrimSpace(socketURL) == "":
		failures = append(failures, "app-level token: Slack returned an empty Socket Mode URL")
	default:
		for i := range result.Permissions {
			if result.Permissions[i].Credential == PermissionCredentialAppToken {
				result.Permissions[i].Granted = true
			}
		}
	}

	result.Connected = len(failures) == 0
	result.Error = strings.Join(failures, "; ")
	return finishConnectionTest(result)
}

func splitScopes(raw string) map[string]bool {
	result := map[string]bool{}
	for _, scope := range strings.Split(raw, ",") {
		if scope = strings.TrimSpace(scope); scope != "" {
			result[scope] = true
		}
	}
	return result
}

// testTelegramConnection validates the token and reports the two @BotFather
// switches plus whether long polling is actually available. baseURL overrides
// the API endpoint for tests.
func testTelegramConnection(ctx context.Context, cfg BotConfig, baseURL string) ConnectionTestResult {
	result := ConnectionTestResult{
		Platform:    PlatformTelegram,
		Permissions: newPermissionChecks(PlatformTelegram),
	}
	if strings.TrimSpace(cfg.BotToken) == "" {
		result.Error = "bot_token is required"
		return finishConnectionTest(result)
	}

	client := newTelegramClient(cfg.BotToken)
	if baseURL != "" {
		client.baseURL = baseURL
	}
	grant := func(key string) {
		for i := range result.Permissions {
			if result.Permissions[i].Key == key {
				result.Permissions[i].Granted = true
			}
		}
	}

	var failures []string
	var me telegramUser
	switch err := client.call(ctx, "getMe", map[string]any{}, &me); {
	case err != nil:
		failures = append(failures, fmt.Sprintf("bot token: %v", err))
	case !me.IsBot || me.ID == 0:
		failures = append(failures, "bot token: this token is not a bot identity")
	default:
		result.Identity = firstNonEmpty("@"+me.Username, me.displayName())
		grant("bot_token")
		if me.CanJoinGroups {
			grant("can_join_groups")
		}
		if me.CanReadAllGroupMessages {
			grant("can_read_all_group_messages")
		}
	}

	// A registered webhook makes getUpdates fail with 409 for as long as it
	// exists, which otherwise surfaces only as a bot that connects and then
	// silently never receives anything.
	var webhook struct {
		URL string `json:"url"`
	}
	switch err := client.call(ctx, "getWebhookInfo", map[string]any{}, &webhook); {
	case err != nil:
		failures = append(failures, fmt.Sprintf("polling: %v", err))
	case strings.TrimSpace(webhook.URL) != "":
		failures = append(failures, fmt.Sprintf("polling: a webhook is registered at %s; delete it so DayMug can use long polling", webhook.URL))
	default:
		grant("polling_available")
	}

	result.Connected = len(failures) == 0
	result.Error = strings.Join(failures, "; ")
	return finishConnectionTest(result)
}

func testFeishuConnection(ctx context.Context, cfg BotConfig, options []lark.ClientOptionFunc) ConnectionTestResult {
	result := ConnectionTestResult{
		Platform:    PlatformFeishu,
		Permissions: newPermissionChecks(PlatformFeishu),
	}
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.AppSecret) == "" {
		result.Error = "app_id and app_secret are required"
		return finishConnectionTest(result)
	}

	client := newFeishuRESTClient(cfg.AppID, cfg.AppSecret, options...)
	connector := NewFeishuConnector(FeishuConfig{
		Enabled: true, BotName: cfg.Name, AppID: cfg.AppID, AppSecret: cfg.AppSecret,
	})
	connector.rest = client

	var failures []string
	openID, err := connector.fetchBotOpenID(ctx)
	switch {
	case err != nil:
		failures = append(failures, fmt.Sprintf("bot identity: %v", err))
	case strings.TrimSpace(openID) == "":
		failures = append(failures, "bot identity: Feishu returned an empty bot open_id")
	default:
		result.Identity = openID
	}

	scopeResponse, err := client.Application.Scope.List(ctx)
	switch {
	case err != nil:
		failures = append(failures, fmt.Sprintf("permissions: %v", err))
	case !scopeResponse.Success():
		failures = append(failures, fmt.Sprintf("permissions: code %d: %s", scopeResponse.Code, scopeResponse.Msg))
	default:
		granted := grantedFeishuScopes(scopeResponse.Data)
		for i := range result.Permissions {
			key := result.Permissions[i].Key
			if key == "im:message:recall" {
				// Feishu accepts any of these three scopes for recalling a
				// message sent by the app. Report the capability separately in
				// the admin UI without demanding a redundant scope grant.
				result.Permissions[i].Granted = granted[key] || granted["im:message"] || granted["im:message:send_as_bot"]
				continue
			}
			result.Permissions[i].Granted = granted[key]
		}
	}

	result.Connected = len(failures) == 0
	result.Error = strings.Join(failures, "; ")
	return finishConnectionTest(result)
}

func grantedFeishuScopes(data *larkapplication.ListScopeRespData) map[string]bool {
	result := map[string]bool{}
	if data == nil {
		return result
	}
	for _, scope := range data.Scopes {
		if scope == nil || scope.ScopeName == nil || scope.GrantStatus == nil || *scope.GrantStatus != 1 {
			continue
		}
		result[strings.TrimSpace(*scope.ScopeName)] = true
	}
	return result
}

// testWeChatConnection reports whether a pairing is complete and still
// honoured. There are no scopes to enumerate: a Weixin bot token is issued by
// a phone confirmation, and the gateway either accepts it or it has expired
// and the account must be paired again. baseURL overrides the endpoint for
// tests; in production it comes from the pairing.
func testWeChatConnection(ctx context.Context, cfg BotConfig, baseURL string) ConnectionTestResult {
	result := ConnectionTestResult{
		Platform:    PlatformWeChat,
		Permissions: newPermissionChecks(PlatformWeChat),
	}
	grant := func(key string) {
		for i := range result.Permissions {
			if result.Permissions[i].Key == key {
				result.Permissions[i].Granted = true
			}
		}
	}

	token := strings.TrimSpace(cfg.BotToken)
	gateway := firstNonEmpty(strings.TrimSpace(baseURL), strings.TrimSpace(cfg.AppID))
	if token == "" || gateway == "" {
		result.Error = "not paired yet: scan the QR code to obtain a bot token and gateway"
		return finishConnectionTest(result)
	}
	grant("paired")

	client := wechat.NewClient(wechat.ClientOptions{BaseURL: gateway, Token: token, BotAgent: wechatBotAgent})
	// notifyStart is the only authenticated call that needs nothing but the
	// token. getConfig looks cheaper but requires an ilink_user_id, which does
	// not exist until somebody has messaged the bot — testing with a blank one
	// fails with "ilink_user_id required" even on a perfectly good pairing.
	//
	// It is also exactly what the connector issues when it comes up, so a
	// passing test proves the same call the real connection depends on. The
	// only side effect is marking the bot online, which starting it would do
	// anyway and which delivers nothing on its own.
	if err := client.NotifyStart(ctx); err != nil {
		result.Error = fmt.Sprintf("gateway rejected the pairing: %v", err)
		return finishConnectionTest(result)
	}
	grant("token_accepted")

	result.Connected = true
	return finishConnectionTest(result)
}
