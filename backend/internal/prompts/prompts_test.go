package prompts

import (
	"strings"
	"testing"
)

// TestGoldenPrompts locks the assembled prompt text verbatim. Any wording
// change — intentional or accidental (e.g. touching a shared fragment) —
// must show up here as an explicit golden update in the same diff.
func TestGoldenPrompts(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "WebArtifact",
			got:  WebArtifact,
			want: `When you create an image or file for the user, append one final line per artifact using exactly:
[DAYMUG_ARTIFACT path/inside/current/working/directory]
Only publish an existing regular file inside the current working directory. Write the file's absolute path, or a path relative to the current working directory that keeps every directory segment — a bare file name publishes a file directly in the working directory and nothing else. If a tool creates the file outside the working directory (for example under /tmp), copy it into the working directory first and publish the copied path. Never publish the outside source path. Do not wrap the marker in a code block. DayMug web displays supported images inline and offers every other file as a download; a linked IM thread receives every declared artifact as an upload.`,
		},
		{
			name: "AbsolutePaths",
			want: `When you show the user where a file is, write its absolute filesystem path as plain text. Never turn a filesystem path into a Markdown link — DayMug serves published files through its own authenticated endpoints, so a host path is not a URL the user can open.`,
			got:  AbsolutePaths,
		},
		{
			name: "IMArtifact",
			got:  IMArtifact,
			want: `When you create an image or file that the user should receive in this chat thread, append one final line per artifact using exactly:
[DAYMUG_ARTIFACT path/inside/current/working/directory]
Only publish an existing regular file inside the current working directory. Write the file's absolute path, or a path relative to the current working directory that keeps every directory segment — a bare file name publishes a file directly in the working directory and nothing else. If a tool creates the file outside the working directory (for example under /tmp), copy it into the working directory first and publish the copied path. Never publish the outside source path. Do not wrap the marker in a code block. DayMug removes the marker and uploads the file through the configured bot identity.`,
		},
		{
			name: "TitleSystem",
			got:  TitleSystem,
			want: `You summarize the start of a conversation into a single short title (under 30 characters). Use the same language as the conversation. If the conversation so far is too thin or generic to produce a meaningful title (e.g. a single-word ping like "hi"), output exactly the literal string SKIP and nothing else — we will retry once more context arrives. Otherwise, output ONLY the title text — no quotes, no trailing punctuation, no prefix, no explanation. Do not use any tools.`,
		},
		{
			name: "TitleGenTemplate",
			got:  TitleGenTemplate,
			want: `Summarize the following conversation start into a single short title (under 30 characters). Use the same language as the conversation. If the conversation so far is too thin or generic to produce a meaningful title (e.g. a single-word ping like "hi"), output exactly the literal string SKIP and nothing else — we will retry once more context arrives. Otherwise, output ONLY the title text — no quotes, no trailing punctuation, no prefix, no explanation. Do not use any tools.

Conversation:
%s`,
		},
		{
			name: "IMSession",
			got:  IMSession("Slack"),
			want: `You are replying inside a Slack thread. Your answer is delivered as plain chat text: keep it concise, avoid heavy Markdown formatting (tables, nested lists), and answer in the language the user wrote in. All outbound platform communication is exclusively handled by DayMug's attached bot connector. Never call any Slack, Feishu, Lark, Telegram, or WeChat/Weixin messaging tool, API, CLI, webhook, or MCP tool to send, reply, edit, react, upload, or post messages, even if the user asks. Never use employee or user credentials for platform communication. Return only the reply text; DayMug will deliver it as the configured bot identity.`,
		},
		{
			name: "IMMention",
			got:  IMMention,
			want: `To @-mention someone in this thread, write @ followed by that person's display name exactly as it appears in this conversation (the name shown in a "[Name]:" prefix or after an @). DayMug converts that into a native platform mention that notifies them. Only people who already appear in this thread can be mentioned; channel-wide pings such as @channel, @here or @everyone never work, and neither does writing the platform's own mention markup yourself. Put the name in backticks when you mean to quote it rather than notify the person.`,
		},
		{
			name: "IMHandoff",
			got:  IMHandoff("@alpha, @beta"),
			want: `DayMug bot handoff is available in this thread. Available targets: @alpha, @beta. If and only if one of them must continue the work, append exactly one final non-empty line in this format:
[HANDOFF @BotName] concise instruction
Use a BotName exactly as listed. If no handoff is needed, do not output a HANDOFF line. Ordinary @mentions do not request a handoff. The instruction must stand on its own: the other agent does not receive your earlier replies, so name the target, the identifiers and the exact ask instead of referring back to what you just wrote.`,
		},
		{
			name: "FeishuHistoryTruncated",
			got:  FeishuHistoryTruncated,
			want: `[DayMug] 该话题更早的历史消息数量过多，已被省略；以下上下文并不完整。若需要更早的内容，请让用户直接补充，不要假设你已看到话题的全部历史。`,
		},
		{
			name: "InboundAttachmentsSkipped",
			got:  InboundAttachmentsSkipped(3),
			want: `[DayMug] 该消息有 3 个附件因超出单轮附件数量/体积上限未被保存，你看到的附件列表并不完整；如果需要缺失的内容，请让用户单独重新发送，不要假设附件已全部就位。`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("prompt drifted from golden text:\n--- got ---\n%s\n--- want ---\n%s", tt.got, tt.want)
			}
		})
	}
}

// TestMarkersAppearInTeachingPrompts binds each parser marker to the prompt
// that teaches it, so renaming a marker without reteaching it fails here.
func TestMarkersAppearInTeachingPrompts(t *testing.T) {
	tests := []struct {
		name   string
		marker string
		prompt string
	}{
		{"artifact marker in WebArtifact", ArtifactMarkerPrefix, WebArtifact},
		{"artifact marker in IMArtifact", ArtifactMarkerPrefix, IMArtifact},
		{"abstain sentinel in TitleSystem", TitleAbstain, TitleSystem},
		{"abstain sentinel in TitleGenTemplate", TitleAbstain, TitleGenTemplate},
		{"handoff marker in IMHandoff", HandoffMarkerPrefix, IMHandoff("@x")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(tt.prompt, tt.marker) {
				t.Errorf("marker %q missing from prompt:\n%s", tt.marker, tt.prompt)
			}
		})
	}
}

func TestUserSystem(t *testing.T) {
	tests := []struct {
		name, role, want string
	}{
		{"role", "dev", "## Your Identity\ndev"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UserSystem(tt.role); got != tt.want {
				t.Errorf("UserSystem(%q) = %q, want %q", tt.role, got, tt.want)
			}
		})
	}
}
