package imbot

// User-visible copy a connector may post into a thread, collected in one
// place so wording changes don't have to chase literals across files. There
// is no backend i18n layer; IM copy ships in Chinese.
//
// Ownership rule: copy that a *connector* emits because of how a platform
// message behaves — an empty reply still needs a body, a typing indicator, a
// chunk budget overflowing — lives here. Copy the bridge emits while
// orchestrating a turn — ack, superseded, queue position, progress phases,
// bot relay — lives in service/imbridge/messages.go.
//
// EmptyReplyText is the one string both layers legitimately need (the
// connector substitutes it for a blank chunk list, the bridge persists it as
// the turn's content), so it stays here as the lower layer and imbridge
// references imbot.EmptyReplyText rather than re-declaring the literal.
const (
	// EmptyReplyText stands in for a successful turn that produced no text.
	// An IM thread must receive something, so the placeholder is both posted
	// and persisted; the web transcript persists nothing in that case.
	EmptyReplyText = "(agent 没有返回内容)"

	// TypingStatusText labels the platform-native activity indicator while a
	// turn is running.
	TypingStatusText = "正在输入…"

	// ReplyTruncatedSuffix ends the final chunk when a reply overflows the
	// per-message chunk budget, so the reader knows content was dropped.
	ReplyTruncatedSuffix = "\n…(内容过长，已截断)"
)
