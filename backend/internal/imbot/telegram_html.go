package imbot

import (
	"regexp"
	"strings"
)

// Telegram offers MarkdownV2 and HTML parse modes. DayMug uses HTML because
// MarkdownV2 requires escaping 18 characters *inside* the agent's own prose,
// and a single unbalanced `*` or `_` — which code, math and file globs produce
// constantly — makes Telegram reject the entire message with HTTP 400. Under
// HTML the only control characters are &, < and >, and the same
// escape-content-then-emit-our-own-markup discipline SlackMrkdwn uses makes an
// injection or a conversion miss impossible to turn into a malformed message.

// telegramTextEscaper neutralizes the three characters Telegram's HTML parse
// mode treats as markup. strings.Replacer scans the input once, so the & it
// emits for &amp; is never re-escaped — the "& first" ordering entity encoding
// requires. NUL is dropped because it cannot survive the JSON transport.
var telegramTextEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	"\x00", "",
)

// telegramHrefEscaper additionally neutralizes the quote that would end an
// href attribute early.
var telegramHrefEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"\x00", "",
)

func escapeTelegramText(s string) string { return telegramTextEscaper.Replace(s) }

// telegramFence matches a fenced code block delimiter, capturing the optional
// language hint Telegram renders as syntax highlighting.
var telegramFence = regexp.MustCompile("^\\s*```\\s*([A-Za-z0-9_+#.-]*)\\s*$")

// TelegramHTML converts the Markdown agents emit into the small tag set
// Telegram's parse_mode=HTML accepts: **bold**/__bold__ → <b>, ~~strike~~ →
// <s>, `code` → <code>, fenced blocks → <pre><code>, [label](url) → <a>, and
// # headings → bold lines. Everything else is escaped literal text, so an
// unrecognized construct degrades to plain prose rather than a rejected
// message.
func TelegramHTML(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))

	var fenced []string
	openTag := ""
	inFence := false
	closeFence := func() {
		out = append(out, openTag+strings.Join(fenced, "\n")+"</code></pre>")
		fenced, openTag, inFence = nil, "", false
	}

	for _, line := range lines {
		if m := telegramFence.FindStringSubmatch(line); m != nil {
			if inFence {
				closeFence()
				continue
			}
			inFence = true
			if lang := m[1]; lang != "" {
				openTag = `<pre><code class="language-` + escapeTelegramText(lang) + `">`
			} else {
				openTag = "<pre><code>"
			}
			continue
		}
		if inFence {
			fenced = append(fenced, escapeTelegramText(line))
			continue
		}
		out = append(out, telegramHTMLLine(line))
	}
	if inFence {
		// An agent reply truncated mid-block leaves the fence open. Telegram
		// rejects unbalanced tags outright, so the closer is synthesized rather
		// than losing the whole message to a 400.
		closeFence()
	}
	return strings.Join(out, "\n")
}

func telegramHTMLLine(line string) string {
	if m := mrkdwnHeading.FindStringSubmatch(line); m != nil {
		return m[1] + "<b>" + telegramSpans(strings.TrimSpace(m[2]), true) + "</b>"
	}
	return telegramSpans(line, false)
}

// telegramSpans splits a line on backticks: odd-indexed segments sit between a
// matched pair and become <code>, while a trailing odd segment has no closing
// backtick and is ordinary text that keeps its literal backtick.
func telegramSpans(line string, inHeading bool) string {
	parts := strings.Split(line, "`")
	var b strings.Builder
	for i, part := range parts {
		if i%2 == 1 && i < len(parts)-1 {
			b.WriteString("<code>")
			b.WriteString(escapeTelegramText(part))
			b.WriteString("</code>")
			continue
		}
		if i%2 == 1 {
			b.WriteString("`")
		}
		b.WriteString(telegramSegment(part, inHeading))
	}
	return b.String()
}

// telegramSegment rebuilds one non-code segment, wrapping Markdown links in
// <a href> around independently escaped components.
func telegramSegment(s string, inHeading bool) string {
	var b strings.Builder
	last := 0
	for _, m := range mrkdwnLink.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(telegramInline(s[last:m[0]], inHeading))
		target := s[m[4]:m[5]]
		label := strings.TrimSpace(s[m[2]:m[3]])
		b.WriteString(`<a href="`)
		b.WriteString(telegramHrefEscaper.Replace(target))
		b.WriteString(`">`)
		b.WriteString(telegramInline(firstNonEmpty(label, target), inHeading))
		b.WriteString("</a>")
		last = m[1]
	}
	b.WriteString(telegramInline(s[last:], inHeading))
	return b.String()
}

func telegramInline(s string, inHeading bool) string {
	s = escapeTelegramText(s)
	s = mrkdwnStrike.ReplaceAllString(s, "<s>$1</s>")
	return mrkdwnBold.ReplaceAllStringFunc(s, func(m string) string {
		inner := strings.TrimSuffix(strings.TrimPrefix(m, "**"), "**")
		inner = strings.TrimSuffix(strings.TrimPrefix(inner, "__"), "__")
		if inHeading {
			// A heading is already wrapped in <b>; Telegram ignores a nested
			// duplicate but the markup is noise either way.
			return inner
		}
		return "<b>" + inner + "</b>"
	})
}
