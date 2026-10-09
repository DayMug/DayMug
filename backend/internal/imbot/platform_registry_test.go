package imbot

import (
	"context"
	"strings"
	"testing"
)

// allCredentials fills every credential slot, so what survives validation is
// exactly the set the platform declares it uses.
func allCredentials(platform string, enabled bool) BotConfig {
	return BotConfig{
		ID: "b1", Platform: platform, Enabled: enabled,
		BotToken: "bot-token", AppToken: "app-token", AppID: "app-id", AppSecret: "app-secret",
	}
}

// Pins, per platform, which credentials an enabled bot must carry, the exact
// refusal an operator sees when one is missing, and which of the four slots
// survive normalisation. Every row is today's behaviour; the registry must not
// move any of it.
func TestValidateBotConfigCredentialsPerPlatform(t *testing.T) {
	tests := []struct {
		platform string
		missing  string
		kept     BotConfig
	}{
		{PlatformSlack, "enabled Slack bots require bot_token and app_token",
			BotConfig{BotToken: "bot-token", AppToken: "app-token"}},
		{PlatformFeishu, "enabled Feishu bots require app_id and app_secret",
			BotConfig{AppID: "app-id", AppSecret: "app-secret"}},
		{PlatformTelegram, "enabled Telegram bots require bot_token",
			BotConfig{BotToken: "bot-token"}},
		{PlatformWeChat, "enabled WeChat bots must be paired first: scan the QR code to obtain a bot token and gateway",
			BotConfig{BotToken: "bot-token", AppID: "app-id"}},
	}
	for _, tt := range tests {
		t.Run(tt.platform, func(t *testing.T) {
			_, err := ValidateBotConfig(BotConfig{ID: "b1", Platform: tt.platform, Enabled: true})
			if err == nil || err.Error() != tt.missing {
				t.Fatalf("enabled bot with no credentials: err = %v, want %q", err, tt.missing)
			}

			if _, err := ValidateBotConfig(BotConfig{ID: "b1", Platform: tt.platform}); err != nil {
				t.Fatalf("a disabled bot may be saved without credentials: %v", err)
			}

			for _, enabled := range []bool{true, false} {
				got, err := ValidateBotConfig(allCredentials(tt.platform, enabled))
				if err != nil {
					t.Fatalf("enabled=%v with every credential: %v", enabled, err)
				}
				if got.BotToken != tt.kept.BotToken || got.AppToken != tt.kept.AppToken ||
					got.AppID != tt.kept.AppID || got.AppSecret != tt.kept.AppSecret {
					t.Fatalf("enabled=%v kept credentials = %+v, want only %+v", enabled,
						[]string{got.BotToken, got.AppToken, got.AppID, got.AppSecret}, tt.kept)
				}
			}

			if !CredentialsComplete(tt.platform, allCredentials(tt.platform, true)) {
				t.Fatal("CredentialsComplete = false with every slot filled")
			}
			if CredentialsComplete(tt.platform, BotConfig{Platform: tt.platform}) {
				t.Fatal("CredentialsComplete = true with nothing filled")
			}
			if !CredentialsComplete(tt.platform, tt.kept) {
				t.Fatalf("CredentialsComplete = false for exactly the kept set %+v", tt.kept)
			}
		})
	}
}

func TestValidateBotConfigRejectsUnknownPlatform(t *testing.T) {
	_, err := ValidateBotConfig(BotConfig{ID: "b1", Platform: "irc"})
	if err == nil || err.Error() != `unknown platform "irc"` {
		t.Fatalf("err = %v, want the unknown-platform refusal", err)
	}
	if CredentialsComplete("irc", allCredentials("irc", true)) {
		t.Fatal("an unknown platform can never have complete credentials")
	}
}

func TestTestConnectionRejectsUnknownPlatform(t *testing.T) {
	got := TestConnection(context.Background(), BotConfig{Platform: "irc"})
	if got.Platform != "irc" || got.Error != `unknown platform "irc"` || got.Connected {
		t.Fatalf("TestConnection(irc) = %+v", got)
	}
}

// Every rule in a channels document is checked against the registry, and the
// empty platform (a rule that applies to whichever platform the bot is on) is
// still accepted.
func TestValidateChannelsAcceptsEveryRegisteredPlatform(t *testing.T) {
	for _, platform := range append(SupportedPlatforms(), "") {
		raw := `[{"channel":"c1","platform":"` + platform + `"}]`
		if _, err := ValidateChannels(raw); err != nil {
			t.Fatalf("platform %q rejected: %v", platform, err)
		}
	}
	_, err := ValidateChannels(`[{"channel":"c1","platform":"irc"}]`)
	if err == nil || !strings.Contains(err.Error(), `unknown platform "irc"`) {
		t.Fatalf("err = %v, want the unknown-platform refusal", err)
	}
}

// The registry claims that adding a platform is one entry plus a connector. That
// claim only holds if every entry is complete, because each field replaces a
// switch that used to fail loudly on a missing case: an entry without a
// connection test, a credential spec or permission requirements would now fail
// silently instead.
func TestEveryRegisteredPlatformIsFullyDescribed(t *testing.T) {
	if len(platforms) == 0 {
		t.Fatal("no platforms registered")
	}
	for id, d := range platforms {
		t.Run(id, func(t *testing.T) {
			if d.caps.DisplayName == "" || d.caps.PromptName == "" || d.caps.SystemPromptName == "" {
				t.Errorf("capabilities are missing a display name: %+v", d.caps)
			}
			if d.factory == nil {
				t.Error("no connector factory")
			}
			if d.testConnection == nil {
				t.Error("no connection test")
			}
			if d.credentials.fields == 0 {
				t.Error("no credentials declared: an enabled bot could be saved with none")
			}
			if strings.TrimSpace(d.credentials.missing) == "" {
				t.Error("no refusal message for missing credentials")
			}
			if len(permissionRequirements[id]) == 0 {
				t.Error("no permission requirements: the connection test would report nothing")
			}
			if c := d.factory(allCredentials(id, true)); c == nil || c.Platform() != id {
				t.Errorf("factory builds a connector for %q, want %q", c.Platform(), id)
			}
		})
	}
	for id := range permissionRequirements {
		if _, ok := platforms[id]; !ok {
			t.Errorf("permission requirements for unregistered platform %q", id)
		}
	}
}
