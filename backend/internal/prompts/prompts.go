// Package prompts is the single home for every LLM-facing prompt string and
// for the marker literals shared between prompt text and the parsers that
// strip them. Splicing each marker into its teaching prompt from the same
// constant makes prompt/parser drift impossible; prompts_test.go locks the
// assembled output so any wording change shows up verbatim in a diff.
//
// This package must stay a leaf: no imports from internal/ so every layer
// (agent, service, handler, imbridge) can depend on it.
package prompts

import (
	"fmt"
	"strings"
)

// Marker literals recognised by parsers elsewhere in the codebase.
const (
	// ArtifactMarkerPrefix opens a "[DAYMUG_ARTIFACT path]" line; parsed by
	// service.ExtractArtifacts.
	ArtifactMarkerPrefix = "[DAYMUG_ARTIFACT "

	// TitleAbstain is the literal a title model emits to decline when the
	// conversation is too thin; recognised by agent.CleanTitle.
	TitleAbstain = "SKIP"

	// HandoffMarkerPrefix opens a "[HANDOFF @BotName] instruction" line;
	// parsed by imbridge's handoff directive parser.
	HandoffMarkerPrefix = "[HANDOFF @"

	// CompactSummaryHeader prefixes the assistant message a /compact run
	// stores, so users scrolling history can tell it from a real reply.
	CompactSummaryHeader = "[Compact summary]"
)

// AbsolutePaths makes file locations unambiguous across web and IM clients.
// Paths stay plain text because a host filesystem path is not a browser URL;
// artifact markers remain the authenticated way to publish downloadable files.
const AbsolutePaths = "When you show the user where a file is, write its absolute filesystem path as plain text. " +
	"Never turn a filesystem path into a Markdown link — DayMug serves published files through its own authenticated endpoints, " +
	"so a host path is not a URL the user can open."

// UserSystem renders the per-agent system prompt from the persona's role
// definition, under a markdown heading. An empty persona yields "".
func UserSystem(roleDefinition string) string {
	if roleDefinition == "" {
		return ""
	}
	return "## Your Identity\n" + roleDefinition
}

// artifactRules is the transport-independent middle of both artifact prompts:
// the marker line itself plus the publishing constraints.
//
// The path rule spells out absolute-is-fine because the previous wording
// ("preferably as a relative path") made models shorten a path they already
// held correctly: a file at <cwd>/.probe/shot.png was published as "shot.png",
// which resolves to <cwd>/shot.png and is rejected as missing. Relativising is
// the step that loses directories, so the prompt no longer asks for it.
const artifactRules = ArtifactMarkerPrefix + `path/inside/current/working/directory]
Only publish an existing regular file inside the current working directory. Write the file's absolute path, or a path relative to the current working directory that keeps every directory segment — a bare file name publishes a file directly in the working directory and nothing else. If a tool creates the file outside the working directory (for example under /tmp), copy it into the working directory first and publish the copied path. Never publish the outside source path. Do not wrap the marker in a code block. `

// WebArtifact teaches browser-originated agents how to explicitly publish
// generated files. The marker is stripped from the visible answer; DayMug
// validates the path and serves it through its authenticated file endpoint
// instead of trusting a model-authored URL.
const WebArtifact = "When you create an image or file for the user, append one final line per artifact using exactly:\n" +
	artifactRules +
	"DayMug web displays supported images inline and offers every other file as a download; a linked IM thread receives every declared artifact as an upload."

// IMArtifact is the transport variant for every IM platform: the attached bot
// connector uploads each declared artifact via the platform API after the text
// reply completes. Phrased platform-neutrally because IMSession already names
// the platform, and a stale list here would just contradict it.
const IMArtifact = "When you create an image or file that the user should receive in this chat thread, append one final line per artifact using exactly:\n" +
	artifactRules +
	"DayMug removes the marker and uploads the file through the configured bot identity."

// titleRules is the shared instruction body of both title prompts, so the two
// delivery forms below cannot drift apart.
const titleRules = `Use the same language as the conversation. If the conversation so far is too thin or generic to produce a meaningful title (e.g. a single-word ping like "hi"), output exactly the literal string ` + TitleAbstain + ` and nothing else — we will retry once more context arrives. Otherwise, output ONLY the title text — no quotes, no trailing punctuation, no prefix, no explanation. Do not use any tools.`

// TitleSystem carries the instruction half of the title prompt as a
// standalone system message, for backends with a separate `--system-prompt`
// flag (claude). Replacing the CLI's default system prompt entirely avoids
// dragging in agent framing, env info, CLAUDE.md auto-discovery, and the tool
// catalog — none of which a 30-character title generator needs.
const TitleSystem = "You summarize the start of a conversation into a single short title (under 30 characters). " + titleRules

// TitleGenTemplate is the combined instruction+transcript form for backends
// that take only a single prompt (codex). %s is replaced with the joined
// transcript.
const TitleGenTemplate = "Summarize the following conversation start into a single short title (under 30 characters). " + titleRules + `

Conversation:
%s`

// IMSession constrains replies delivered as plain chat text in an IM thread
// and forbids the agent from talking to the platform APIs itself — outbound
// delivery is exclusively the attached bot connector's job. platform is the
// user-facing platform name, e.g. "Slack" or "飞书 (Feishu/Lark)".
func IMSession(platform string) string {
	return "You are replying inside a " + platform + " thread. Your answer is delivered as plain chat text: " +
		"keep it concise, avoid heavy Markdown formatting (tables, nested lists), and answer in the language the user wrote in. " +
		"All outbound platform communication is exclusively handled by DayMug's attached bot connector. " +
		"Never call any Slack, Feishu, Lark, Telegram, or WeChat/Weixin messaging tool, API, CLI, webhook, or MCP tool to send, reply, edit, react, upload, or post messages, even if the user asks. " +
		"Never use employee or user credentials for platform communication. Return only the reply text; DayMug will deliver it as the configured bot identity."
}

// IMMention teaches the only way an agent can produce a mention that actually
// notifies someone. Its own mention markup never survives: outbound text is
// escaped so a model cannot spell a live channel-wide ping, so DayMug resolves
// names instead — and only for people it already saw in this thread. Appended
// by the bridge on the platforms whose Capabilities claim ResolvesNameMentions.
const IMMention = "To @-mention someone in this thread, write @ followed by that person's display name exactly as it appears in this conversation " +
	"(the name shown in a \"[Name]:\" prefix or after an @). DayMug converts that into a native platform mention that notifies them. " +
	"Only people who already appear in this thread can be mentioned; channel-wide pings such as @channel, @here or @everyone never work, " +
	"and neither does writing the platform's own mention markup yourself. " +
	"Put the name in backticks when you mean to quote it rather than notify the person."

// IMHandoff advertises the bots an agent may hand the thread to and teaches
// the handoff marker line. targets is the ready-joined list of "@Name"
// mentions, e.g. "@alpha, @beta".
func IMHandoff(targets string) string {
	return "DayMug bot handoff is available in this thread. Available targets: " + targets + ". " +
		"If and only if one of them must continue the work, append exactly one final non-empty line in this format:\n" +
		HandoffMarkerPrefix + "BotName] concise instruction\n" +
		"Use a BotName exactly as listed. If no handoff is needed, do not output a HANDOFF line. " +
		"Ordinary @mentions do not request a handoff. " +
		// The receiving agent is given this instruction and the human messages
		// of the thread, not your own replies, so an instruction that leans on
		// "as I explained above" arrives with nothing above it.
		"The instruction must stand on its own: the other agent does not receive your earlier replies, " +
		"so name the target, the identifiers and the exact ask instead of referring back to what you just wrote."
}

// CompactSummarization is the instruction /compact hands the existing
// session. Phrased to bias toward decisions and current state — what a fresh
// session needs to keep working — over chronological retelling.
const CompactSummarization = `Please write a concise summary of our conversation so far. Focus on:
- The user's overall goal and any explicit constraints
- Key decisions reached and code changes already made
- Open questions or unfinished steps the next turn should pick up

Aim for a few short paragraphs of plain prose, no bullet headings unless they make a point clearer. Skip greetings and meta-commentary; the summary will be fed back to a fresh session as starting context.`

// Inbound completeness notices. These are not instructions the way the
// prompts above are — they ride inline in the transcript as synthetic
// messages — but they are still text a model reads and reasons over, which is
// what puts them in this package. Unlike the instruction prompts they are
// written in Chinese: they sit next to the user's own IM messages, so they
// follow the IM copy language rather than the system-prompt language.

// FeishuHistoryTruncated is injected as a synthetic history entry when a
// thread backfill hits its bounds. The agent must not mistake a truncated
// window for the whole thread, and a log line would only reach the operator.
const FeishuHistoryTruncated = "[DayMug] 该话题更早的历史消息数量过多，已被省略；以下上下文并不完整。" +
	"若需要更早的内容，请让用户直接补充，不要假设你已看到话题的全部历史。"

// ArtifactUploadFailed reports files the agent tried to publish that the reader
// will not receive — either the platform refused the upload or the marker never
// resolved to a usable file. The reader is told which files and why, because
// the answer above usually talks about them as if they had arrived, and before
// this notice existed the only trace was a line in the server log.
func ArtifactUploadFailed(failures []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⚠️ %d 个文件未能送达：", len(failures))
	for _, failure := range failures {
		b.WriteString("\n• ")
		b.WriteString(failure)
	}
	return b.String()
}

// InboundAttachmentsSkipped tells the agent that the attachment list it
// received is incomplete, the way FeishuHistoryTruncated does for history.
// skipped is the number of attachments dropped by the per-turn caps.
func InboundAttachmentsSkipped(skipped int) string {
	return fmt.Sprintf("[DayMug] 该消息有 %d 个附件因超出单轮附件数量/体积上限未被保存，"+
		"你看到的附件列表并不完整；如果需要缺失的内容，请让用户单独重新发送，不要假设附件已全部就位。", skipped)
}
