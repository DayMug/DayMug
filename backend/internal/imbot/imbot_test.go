package imbot

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/slack-go/slack"
)

func boolPtr(b bool) *bool { return &b }

func TestRuleFor(t *testing.T) {
	rules := []ChannelRule{
		{Platform: "slack", Channel: "C123", AutoReply: true},
		{Platform: "feishu", Channel: "oc_abc", ExtraPrompt: "ops channel"},
		{Channel: "C999", Enabled: boolPtr(false)},
		{Platform: "feishu", Channel: "dm", Enabled: boolPtr(false)},
	}

	tests := []struct {
		name          string
		platform      string
		channel       string
		isDM          bool
		wantOK        bool
		wantMention   bool
		wantAutoReply bool
	}{
		{"configured slack auto-reply channel", "slack", "C123", false, true, false, true},
		{"configured feishu mention channel", "feishu", "oc_abc", false, true, true, false},
		{"platform mismatch falls through", "feishu", "C123", false, false, false, false},
		{"explicitly disabled channel", "slack", "C999", false, false, false, false},
		{"unconfigured group channel is ignored", "slack", "C777", false, false, false, false},
		// DMs are opt-in: they bypass require_mention and reach a shell-capable
		// agent, so an unconfigured DM must stay silent like any other channel.
		{"unconfigured DM is ignored", "slack", "D111", true, false, false, false},
		{"dm rule can disable DMs", "feishu", "p2p_chat", true, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule, ok := RuleFor(rules, tt.platform, tt.channel, tt.isDM)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got := rule.MentionRequired(); got != tt.wantMention {
				t.Errorf("MentionRequired() = %v, want %v", got, tt.wantMention)
			}
			if rule.AutoReply != tt.wantAutoReply {
				t.Errorf("AutoReply = %v, want %v", rule.AutoReply, tt.wantAutoReply)
			}
		})
	}
}

func TestChannelRuleDefaults(t *testing.T) {
	r := ChannelRule{Channel: "C1"}
	if !r.IsEnabled() || !r.MentionRequired() {
		t.Fatalf("zero-value rule should be enabled and mention-required; got %v %v",
			r.IsEnabled(), r.MentionRequired())
	}
	auto := ChannelRule{Channel: "C1", AutoReply: true, RequireMention: boolPtr(true)}
	if auto.MentionRequired() {
		t.Fatal("auto_reply must override require_mention")
	}
}

func TestChannelRuleAllowsSender(t *testing.T) {
	tests := []struct {
		name     string
		rule     ChannelRule
		senderID string
		want     bool
	}{
		{name: "empty allowlist permits every sender", rule: ChannelRule{}, senderID: "U1", want: true},
		{name: "listed sender is permitted", rule: ChannelRule{AllowedUserIDs: []string{"U1"}}, senderID: "U1", want: true},
		{name: "unlisted sender is rejected", rule: ChannelRule{AllowedUserIDs: []string{"U1"}}, senderID: "U2", want: false},
		{name: "wildcard permits every sender", rule: ChannelRule{AllowedUserIDs: []string{"*"}}, senderID: "U2", want: true},
		{name: "wildcard tolerates surrounding whitespace", rule: ChannelRule{AllowedUserIDs: []string{" * "}}, senderID: "U2", want: true},
		{name: "configured ids tolerate surrounding whitespace", rule: ChannelRule{AllowedUserIDs: []string{" U1 "}}, senderID: "U1", want: true},
		{name: "blank configured id does not permit missing sender", rule: ChannelRule{AllowedUserIDs: []string{" "}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rule.AllowsSender(tt.senderID); got != tt.want {
				t.Fatalf("AllowsSender(%q) = %v, want %v", tt.senderID, got, tt.want)
			}
		})
	}
}

func TestChannelRuleRelayDefaults(t *testing.T) {
	r := ChannelRule{Channel: "C1"}
	if r.AllowBotMentions || r.RelayLimit() != 3 || r.RelayWindow() != 30*time.Minute {
		t.Fatalf("relay defaults: allow=%v limit=%d window=%s", r.AllowBotMentions, r.RelayLimit(), r.RelayWindow())
	}
	limit, minutes := 5, 10
	r = ChannelRule{Channel: "C1", AllowBotMentions: true, BotMentionLimit: &limit, BotMentionWindowMinutes: &minutes}
	if r.RelayLimit() != 5 || r.RelayWindow() != 10*time.Minute {
		t.Fatalf("relay overrides: limit=%d window=%s", r.RelayLimit(), r.RelayWindow())
	}
}

func TestRuleForWildcardAndExactPrecedence(t *testing.T) {
	disabled := false
	rules := []ChannelRule{
		{Platform: PlatformSlack, Channel: "*", AutoReply: true},
		{Platform: PlatformSlack, Channel: "C-private", Enabled: &disabled},
	}
	if rule, ok := RuleFor(rules, PlatformSlack, "C-any", false); !ok || !rule.AutoReply {
		t.Fatalf("wildcard did not match: %+v %v", rule, ok)
	}
	if _, ok := RuleFor(rules, PlatformSlack, "C-private", false); ok {
		t.Fatal("exact disabled rule must override wildcard")
	}
	if _, ok := RuleFor(rules, PlatformFeishu, "oc-any", false); ok {
		t.Fatal("wildcard must remain platform scoped")
	}
}

func TestMatchRuleSeparatesUnconfiguredFromDisabled(t *testing.T) {
	disabled := false
	rules := []ChannelRule{
		{Platform: PlatformSlack, Channel: "*"},
		{Platform: PlatformSlack, Channel: "C-off", Enabled: &disabled},
		{Platform: PlatformSlack, Channel: "dm", Enabled: &disabled},
	}
	if rule, matched := MatchRule(rules, PlatformSlack, "C-off", false); !matched || rule.IsEnabled() {
		t.Fatalf("disabled exact rule should still match: %+v matched=%v", rule, matched)
	}
	if _, matched := MatchRule(rules, PlatformFeishu, "oc-unknown", false); matched {
		t.Fatal("a platform-scoped wildcard must not cover another platform")
	}
	if rule, matched := MatchRule(rules, PlatformSlack, "D1", true); !matched || rule.IsEnabled() {
		t.Fatalf("disabled dm rule should still match: %+v matched=%v", rule, matched)
	}
}

func TestValidateBotConfigNormalizesOnePlatform(t *testing.T) {
	bot, err := ValidateBotConfig(BotConfig{ID: "bot-1", Name: "Work", Platform: "SLACK", Enabled: true,
		BotToken: " xoxb ", AppToken: " xapp ", AppID: "discard", Channels: []ChannelRule{{Channel: "*"}}})
	if err != nil {
		t.Fatal(err)
	}
	if bot.Platform != PlatformSlack || bot.BotToken != "xoxb" || bot.AppID != "" || bot.Channels[0].Platform != PlatformSlack {
		t.Fatalf("bot was not normalized: %+v", bot)
	}
}

func TestValidateChannels(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantN   int
		wantErr string
	}{
		{"empty document", "", 0, ""},
		{"valid rules", `[{"channel":"C1","auto_reply":true},{"platform":"feishu","channel":"oc_x"}]`, 2, ""},
		{"not an array", `{"channel":"C1"}`, 0, "JSON array"},
		{"missing channel", `[{"platform":"slack"}]`, 0, "channel"},
		{"bad platform", `[{"platform":"discord","channel":"C1"}]`, 0, "unknown platform"},
		{"relay rule", `[{"channel":"C1","allow_bot_mentions":true,"bot_mention_limit":2,"bot_mention_window_minutes":15}]`, 1, ""},
		{"negative relay limit", `[{"channel":"C1","bot_mention_limit":-1}]`, 0, "bot_mention_limit"},
		{"zero relay window", `[{"channel":"C1","bot_mention_window_minutes":0}]`, 0, "bot_mention_window_minutes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rules, err := ValidateChannels(tt.raw)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(rules) != tt.wantN {
					t.Fatalf("got %d rules, want %d", len(rules), tt.wantN)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want contains %q", err, tt.wantErr)
			}
		})
	}
}

func TestSplitReply(t *testing.T) {
	if got := SplitReply("  ", 100, 5); got != nil {
		t.Fatalf("blank input should yield nil, got %v", got)
	}
	if got := SplitReply("short", 100, 5); len(got) != 1 || got[0] != "short" {
		t.Fatalf("short input should be one chunk, got %v", got)
	}

	long := strings.Repeat("line one\nline two\n", 100) // 1800 bytes
	chunks := SplitReply(long, 500, 10)
	if len(chunks) < 3 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 500 {
			t.Errorf("chunk %d exceeds limit: %d bytes", i, len(c))
		}
	}

	// Multi-byte text without newlines must never split mid-rune.
	cjk := strings.Repeat("多字节字符", 200)
	for i, c := range SplitReply(cjk, 100, 50) {
		if !utf8.ValidString(c) {
			t.Fatalf("chunk %d split a rune: %q", i, c[:12])
		}
	}

	// Overflow past maxChunks is truncated with a marker.
	trunc := SplitReply(strings.Repeat("x", 1000), 100, 2)
	if len(trunc) != 2 || !strings.Contains(trunc[1], "截断") {
		t.Fatalf("expected 2 chunks with truncation marker, got %v", trunc)
	}
}

func TestDedup(t *testing.T) {
	d := newDedup(3)
	for _, key := range []string{"a", "b", "c"} {
		if !d.add(key) {
			t.Fatalf("first add of %q should succeed", key)
		}
	}
	if d.add("a") {
		t.Fatal("duplicate within capacity must be rejected")
	}
	// "d" evicts "a"; a redelivery of "a" is then (acceptably) seen as new.
	if !d.add("d") {
		t.Fatal("new key past capacity should succeed")
	}
	if d.add("d") || d.add("b") {
		t.Fatal("still-tracked keys must stay deduplicated")
	}
}

func TestParseFeishuText(t *testing.T) {
	if got := parseFeishuText(`{"text":"@_user_1 hello"}`); got != "@_user_1 hello" {
		t.Fatalf("got %q", got)
	}
	if got := parseFeishuText(`not json`); got != "" {
		t.Fatalf("malformed content should yield empty, got %q", got)
	}
}

func TestParseFeishuImageKey(t *testing.T) {
	if got := parseFeishuImageKey(`{"image_key":"img_v2_test"}`); got != "img_v2_test" {
		t.Fatalf("got %q", got)
	}
	if got := parseFeishuImageKey(`not json`); got != "" {
		t.Fatalf("malformed content should yield empty, got %q", got)
	}
}

func TestSlackAttachments(t *testing.T) {
	connector := &SlackConnector{}
	attachments := connector.slackAttachments([]slack.File{
		{ID: "F1", Name: "photo.png", Mimetype: "image/png", Size: 123, URLPrivateDownload: "https://files.slack.test/photo"},
		{ID: "F2", Name: "notes.txt", Mimetype: "text/plain", URLPrivateDownload: "https://files.slack.test/notes"},
	})
	if len(attachments) != 2 {
		t.Fatalf("got %d attachments, want 2", len(attachments))
	}
	if attachments[0].ID != "F1" || attachments[0].Name != "photo.png" || attachments[0].Download == nil {
		t.Fatalf("unexpected attachment: %+v", attachments[0])
	}
	if attachments[1].ID != "F2" || attachments[1].Name != "notes.txt" || attachments[1].MIME != "text/plain" {
		t.Fatalf("ordinary file not normalized: %+v", attachments[1])
	}
}

func TestSlackEmitAllowsAttachmentOnlyMessage(t *testing.T) {
	var got Message
	connector := &SlackConnector{}
	connector.SetOnMessage(func(msg Message) { got = msg })
	connector.emit(context.Background(), Message{Attachments: []Attachment{{ID: "F1"}}})
	if len(got.Attachments) != 1 {
		t.Fatal("attachment-only Slack message was dropped")
	}
}

func TestParseFeishuFile(t *testing.T) {
	key, name := parseFeishuFile(`{"file_key":"file_v2_test","file_name":"brief.pdf"}`)
	if key != "file_v2_test" || name != "brief.pdf" {
		t.Fatalf("got key=%q name=%q", key, name)
	}
	if key, name := parseFeishuFile(`not json`); key != "" || name != "" {
		t.Fatalf("malformed content should yield zero values: %q %q", key, name)
	}
}
