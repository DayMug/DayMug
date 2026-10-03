package imbot

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type SlackConfig struct {
	Enabled bool `json:"enabled"`
	// BotToken is the xoxb- bot token; AppToken is the xapp- app-level token
	// with connections:write, required for Socket Mode.
	BotToken string `json:"bot_token"`
	AppToken string `json:"app_token"`
}

// TelegramConfig carries the single credential a Bot API connection needs: the
// token @BotFather issues, which authenticates both polling and sending.
type TelegramConfig struct {
	Enabled  bool   `json:"enabled"`
	BotToken string `json:"bot_token"`
}

// WeChatConfig carries what a QR pairing produced. Both halves are needed:
// the token authenticates, and BaseURL routes to the gateway the pairing
// assigned this account — which is not the gateway the QR was fetched from.
type WeChatConfig struct {
	Enabled  bool   `json:"enabled"`
	BotToken string `json:"bot_token"`
	BaseURL  string `json:"base_url"`
}

type FeishuConfig struct {
	Enabled   bool   `json:"enabled"`
	BotName   string `json:"bot_name,omitempty"`
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
}

// ChannelRule configures how the bot behaves in one channel/群. Channels
// without a matching rule are ignored entirely (DMs are the exception — see
// RuleFor). Modeled after openclaw's per-channel policy.
type ChannelRule struct {
	// Platform scopes the rule ("slack" / "feishu"); empty matches both.
	Platform string `json:"platform,omitempty"`
	// Channel is the platform channel id (Slack C…/G…/D…, 飞书 oc_…). The
	// special value "dm" matches every direct chat on the rule's platform.
	Channel string `json:"channel"`
	Enabled *bool  `json:"enabled,omitempty"` // default true
	// RequireMention: only respond when the bot is @-mentioned (default true
	// in group chats; mentions are irrelevant in DMs).
	RequireMention *bool `json:"require_mention,omitempty"`
	// AutoReply responds to every message in the channel, overriding
	// RequireMention.
	AutoReply bool `json:"auto_reply,omitempty"`
	// ExtraPrompt is appended to the agent's system prompt for this channel.
	ExtraPrompt string `json:"extra_prompt,omitempty"`
	// AllowedUserIDs limits human-triggered runs to these platform user ids.
	// Empty allows every human sender. Bot relays use their own gate.
	AllowedUserIDs []string `json:"allowed_user_ids,omitempty"`
	// AgentID pins the DayMug agent that serves this channel. Empty
	// auto-provisions a per-channel agent (source = platform) on first use.
	AgentID string `json:"agent_id,omitempty"`
	// AllowBotMentions opts this channel into bot-to-bot relays: a message
	// authored by another bot triggers a run only when this is true AND the
	// message @-mentions this bot. Default false.
	AllowBotMentions bool `json:"allow_bot_mentions,omitempty"`
	// BotMentionLimit caps how many bot-triggered runs one thread may start
	// within the sliding BotMentionWindowMinutes window — the relay circuit
	// breaker. Default 3; human messages are never limited.
	BotMentionLimit *int `json:"bot_mention_limit,omitempty"`
	// BotMentionWindowMinutes is the sliding window for BotMentionLimit.
	// Default 30.
	BotMentionWindowMinutes *int `json:"bot_mention_window_minutes,omitempty"`
}

const (
	defaultBotMentionLimit         = 3
	defaultBotMentionWindowMinutes = 30
)

func (r ChannelRule) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }
func (r ChannelRule) MentionRequired() bool {
	return !r.AutoReply && (r.RequireMention == nil || *r.RequireMention)
}

// AllowsSender reports whether a human sender may trigger this channel rule.
func (r ChannelRule) AllowsSender(senderID string) bool {
	if len(r.AllowedUserIDs) == 0 {
		return true
	}
	for _, allowed := range r.AllowedUserIDs {
		allowed = strings.TrimSpace(allowed)
		if allowed == "*" || (allowed != "" && allowed == senderID) {
			return true
		}
	}
	return false
}

// RelayLimit is the effective bot-triggered-run cap per thread and window.
func (r ChannelRule) RelayLimit() int {
	if r.BotMentionLimit != nil {
		return *r.BotMentionLimit
	}
	return defaultBotMentionLimit
}

// RelayWindow is the sliding window RelayLimit applies to.
func (r ChannelRule) RelayWindow() time.Duration {
	minutes := defaultBotMentionWindowMinutes
	if r.BotMentionWindowMinutes != nil {
		minutes = *r.BotMentionWindowMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// BotConfig is one independently connected Slack or Feishu app owned by a
// DayMug agent. ID is stable across edits and scopes message/thread ids when
// one agent connects multiple apps whose channel ids happen to overlap.
type BotConfig struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Enabled  bool   `json:"enabled"`

	BotToken  string `json:"bot_token,omitempty"`
	AppToken  string `json:"app_token,omitempty"`
	AppID     string `json:"app_id,omitempty"`
	AppSecret string `json:"app_secret,omitempty"`

	Channels []ChannelRule `json:"channels"`
}

type AgentBotConfig struct {
	AgentID string    `json:"agent_id"`
	Bot     BotConfig `json:"bot"`
}

type Config struct {
	Bots []AgentBotConfig `json:"bots"`
}

// RuleFor reports the enabled rule that governs a channel. MatchRule separates
// an uncovered channel from one an operator explicitly disabled.
func RuleFor(rules []ChannelRule, platform, channelID string, isDM bool) (ChannelRule, bool) {
	rule, matched := MatchRule(rules, platform, channelID, isDM)
	return rule, matched && rule.IsEnabled()
}

// MatchRule resolves exact channel rules before the DM and "*" fallbacks. The
// wildcard applies to every group channel on the bot's platform; an explicit
// channel rule always wins even when it disables that channel. The bool reports
// whether a rule covers the channel, independent of its enabled state.
func MatchRule(rules []ChannelRule, platform, channelID string, isDM bool) (ChannelRule, bool) {
	var wildcard *ChannelRule
	var dm *ChannelRule
	for i := range rules {
		r := rules[i]
		if r.Platform != "" && r.Platform != platform {
			continue
		}
		if r.Channel == channelID {
			return r, true
		}
		if r.Channel == "*" {
			wildcard = &rules[i]
		}
		if r.Channel == "dm" {
			dm = &rules[i]
		}
	}
	if isDM && dm != nil {
		return *dm, true
	}
	if !isDM && wildcard != nil {
		return *wildcard, true
	}
	// No matching rule means no reply — for DMs as well as group channels.
	// A DM is the most privileged surface a bot has: it skips require_mention
	// entirely, and the agent behind it runs shell commands as the DayMug
	// server user. Defaulting it open meant anyone who could find the bot in
	// the workspace directory had that reach without the operator ever writing
	// a rule. Opening a DM is now an explicit act: add a `dm` rule, and scope
	// it with allowed_user_ids.
	return ChannelRule{}, false
}

// ValidateChannels parses and sanity-checks a channel-rules JSON document as
// submitted by the admin UI, returning the normalized rules.
func ValidateChannels(raw string) ([]ChannelRule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var rules []ChannelRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, fmt.Errorf("channels must be a JSON array of rules: %w", err)
	}
	for i, r := range rules {
		if strings.TrimSpace(r.Channel) == "" {
			return nil, fmt.Errorf("rule #%d: \"channel\" is required", i+1)
		}
		if _, ok := platforms[r.Platform]; r.Platform != "" && !ok {
			return nil, fmt.Errorf("rule #%d: unknown platform %q", i+1, r.Platform)
		}
		if r.BotMentionLimit != nil && *r.BotMentionLimit < 0 {
			return nil, fmt.Errorf("rule #%d: bot_mention_limit must be >= 0", i+1)
		}
		if r.BotMentionWindowMinutes != nil && *r.BotMentionWindowMinutes <= 0 {
			return nil, fmt.Errorf("rule #%d: bot_mention_window_minutes must be > 0", i+1)
		}
	}
	return rules, nil
}

// ValidateBotConfig normalizes and validates the one platform connection owned
// by a first-class Bot subject.
func ValidateBotConfig(bot BotConfig) (BotConfig, error) {
	bot.ID = strings.TrimSpace(bot.ID)
	bot.Name = strings.TrimSpace(bot.Name)
	bot.Platform = strings.ToLower(strings.TrimSpace(bot.Platform))
	bot.BotToken = strings.TrimSpace(bot.BotToken)
	bot.AppToken = strings.TrimSpace(bot.AppToken)
	bot.AppID = strings.TrimSpace(bot.AppID)
	bot.AppSecret = strings.TrimSpace(bot.AppSecret)
	if bot.ID == "" {
		return BotConfig{}, errors.New("bot id is required")
	}
	d, ok := platforms[bot.Platform]
	if !ok {
		return BotConfig{}, fmt.Errorf("unknown platform %q", bot.Platform)
	}
	if bot.Enabled && !d.credentials.complete(bot) {
		return BotConfig{}, errors.New(d.credentials.missing)
	}
	bot = d.credentials.strip(bot)
	for i := range bot.Channels {
		r := &bot.Channels[i]
		r.Channel = strings.TrimSpace(r.Channel)
		if r.Channel == "" {
			return BotConfig{}, fmt.Errorf("rule #%d: channel is required", i+1)
		}
		if r.Platform != "" && r.Platform != bot.Platform {
			return BotConfig{}, fmt.Errorf("rule #%d: platform must be %q", i+1, bot.Platform)
		}
		if strings.TrimSpace(r.AgentID) != "" {
			return BotConfig{}, fmt.Errorf("rule #%d: agent_id is not allowed", i+1)
		}
		r.Platform = bot.Platform
	}
	return bot, nil
}

// SplitReply chunks a long agent reply into IM-postable pieces, preferring
// newline boundaries. Slack rejects text messages past ~40k chars and huge
// walls of text are unreadable anyway; past maxChunks the tail is dropped
// with a truncation marker.
func SplitReply(text string, chunkSize, maxChunks int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var chunks []string
	remaining := text
	for remaining != "" && len(chunks) < maxChunks {
		if len(remaining) <= chunkSize {
			chunks = append(chunks, remaining)
			remaining = ""
			break
		}
		cut := strings.LastIndex(remaining[:chunkSize], "\n")
		if cut < chunkSize/2 {
			// No usable newline — cut at chunkSize, backed off to a rune
			// boundary so multi-byte characters never split across chunks.
			cut = chunkSize
			for cut > 0 && !utf8.RuneStart(remaining[cut]) {
				cut--
			}
		}
		chunks = append(chunks, strings.TrimSpace(remaining[:cut]))
		remaining = strings.TrimSpace(remaining[cut:])
	}
	if remaining != "" && len(chunks) > 0 {
		chunks[len(chunks)-1] += ReplyTruncatedSuffix
	}
	return chunks
}
