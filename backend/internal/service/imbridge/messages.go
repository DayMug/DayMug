package imbridge

// User-visible copy the bridge posts into IM threads, collected in one place
// so wording changes don't have to chase literals across files.
//
// Ownership rule: everything here is *orchestration* copy — it exists because
// the bridge sequences a turn (ack, progress phases, superseded, queueing,
// bot relay), not because a platform message needs a body. Connector-level
// copy (empty reply, typing status, chunk truncation) lives in imbot.
//
// The one shared string is imbot.EmptyReplyText: the connector substitutes it
// for a blank reply and the bridge persists it as the turn's content, so both
// layers need it. It stays in imbot as the lower layer and is referenced from
// here as imbot.EmptyReplyText — never re-declared, or the two copies drift.
const (
	// AckText is the placeholder posted into the thread once a message passes
	// gating, so the sender knows the (potentially minutes-long) run started.
	AckText = "🤖 已收到，正在处理…"

	// supersededText replaces the progress message when a newer message in
	// the same thread preempts the running turn.
	supersededText = "⏹️ 已被同一话题中的新消息取消"

	// queueStatusTextf announces the message's position while it waits for an
	// account slot; formatted with (tasks ahead, tasks running).
	queueStatusTextf = "⏳ 我当前在排队，前面共有 %d 个任务，其中 %d 个正在执行。"

	// errorNoticePrefix marks a failure notice posted in place of a reply.
	errorNoticePrefix = "⚠️ "
)

// Progress-message phases. The bridge keeps one message per turn and rewrites
// it in place as a banner on the first line plus the preview below it, so each
// of these is a complete banner rather than a fragment. One banner per phase —
// a second string for "the same phase, but now with a preview" would read as
// the status changing when only the body did.
const (
	// thinkingStatusText is the banner shown from slot acquisition until the
	// agent produces reply text.
	thinkingStatusText = "💭 正在思考…"

	// toolCallStatusText marks the agent entering a tool call.
	toolCallStatusText = "🛠️ 正在调用工具…"

	// replyStatusText is the banner shown once the agent starts producing
	// reply text.
	replyStatusText = "🤖 正在生成回复…"

	// finalizingStatusText marks the wrap-up after the agent's last event.
	finalizingStatusText = "🤖 正在整理回复…"

	// thinkingSectionLabel and replySectionLabel head the two halves of the
	// preview once the turn has produced both. Lenticular brackets rather than
	// Markdown emphasis: the preview is handed to each platform's Markdown
	// renderer, and a label must not depend on that conversion to read as a
	// label.
	thinkingSectionLabel = "【推理】"
	replySectionLabel    = "【回复】"
)

// Web-mirror copy: a browser-originated turn mirrored into the linked thread
// so the IM side sees what the user did on the web.
const (
	// webMirrorPromptPrefix labels the mirrored prompt in the thread.
	webMirrorPromptPrefix = "🌐 DayMug Web:\n"

	// webMirrorCancelledText replaces the mirrored progress message when the
	// user cancels the run from the web UI.
	webMirrorCancelledText = "⏹️ 已取消"

	// cronMirrorPromptPrefix labels a scheduled task's prompt in the thread.
	// Distinct from the web prefix because nobody in the thread just typed it —
	// without the label the message reads as a human asking a question.
	cronMirrorPromptPrefix = "⏰ DayMug 定时任务:\n"
)

// Bot relay copy: what a thread sees when one bot hands work to another.
const (
	// relayLimitReachedTextf is the circuit-breaker notice posted at most once
	// per window; formatted with (relay limit, window minutes).
	relayLimitReachedTextf = "⛔ 机器人接力已达上限（%d 次 / %d 分钟），请人工介入。"

	// relayHandoffBodyTextf is the synthetic inbound message handed to the
	// target bot on platforms that hide bot-authored messages from other bots;
	// formatted with (source bot name, instruction, quoted source reply).
	relayHandoffBodyTextf = "来自 %s 的接力请求：\n\n任务：%s\n\n上一位机器人的回复：\n%s"

	// unknownRelaySenderName stands in when the source bot's display name is
	// unavailable.
	unknownRelaySenderName = "另一个机器人"

	// relayQuoteTruncatedSuffix ends the quoted source reply when it is cut to
	// keep the handoff within one message.
	relayQuoteTruncatedSuffix = "\n…(已截断)"
)
