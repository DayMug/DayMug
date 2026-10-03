package imbot

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

// slackRawMarker delimits Slack markup DayMug composes itself, currently only
// the bot mention tag returned by SlackConnector.MentionTag: everything else
// on the way out is agent content and gets escaped. The marker is a per-process
// nonce around a NUL, so agent output cannot spell it — and escapeSlackText
// drops stray NULs from content anyway. The delimited payload must still look
// like a bare user mention before it is passed through.
var slackRawMarker = newSlackRawMarker()

var slackRawMention = regexp.MustCompile(`^<@[UWB][A-Z0-9]+>$`)

func newSlackRawMarker() string {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		// Without entropy the marker is still NUL-delimited, which agent text
		// never survives with: escapeSlackText strips NUL from content.
		return "\x00daymug\x00"
	}
	return "\x00" + hex.EncodeToString(nonce[:]) + "\x00"
}

// SlackMrkdwn converts the common Markdown constructs agents emit into Slack's
// mrkdwn dialect: **bold**/__bold__ → *bold*, ~~strike~~ → ~strike~,
// [label](url) → <url|label>, and # headings → bold lines. Fenced code blocks
// and inline code spans keep their content verbatim; everything unrecognized is
// left as-is, so a conversion miss degrades to the previous plain-text look.
//
// Slack parses &, < and > as control characters across the whole text object —
// <!channel>, <@U0123> and <url|label> are all built from them — and code spans
// only suppress mrkdwn *styling*, not that parse. Agent text is therefore HTML
// escaped first (& before < and >, in one pass), and the markers this function
// generates itself are inserted afterwards. A literal "<!channel>" in a reply
// can never turn into a live ping, and Slack decodes the entities back for
// display, inside code spans included.
func SlackMrkdwn(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		convert := mrkdwnLine
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			convert = escapeSlackText
		} else if inFence {
			convert = escapeSlackText
		}
		out = append(out, mrkdwnRawAware(line, convert))
	}
	return strings.Join(out, "\n")
}

// mrkdwnRawAware converts the agent-authored parts of a line and lets the
// markup DayMug delimited itself through verbatim, markers removed.
func mrkdwnRawAware(line string, convert func(string) string) string {
	if !strings.Contains(line, slackRawMarker) {
		return convert(line)
	}
	parts := strings.Split(line, slackRawMarker)
	for i, part := range parts {
		if i%2 == 1 && i < len(parts)-1 && slackRawMention.MatchString(part) {
			continue
		}
		parts[i] = convert(part)
	}
	return strings.Join(parts, "")
}

var (
	mrkdwnHeading = regexp.MustCompile(`^(\s*)#{1,6}\s+(.+)$`)
	mrkdwnQuote   = regexp.MustCompile(`^\s*>+ ?`)
	mrkdwnLink    = regexp.MustCompile(`!?\[([^\]]*)\]\(([^)\s]+)\)`)
	mrkdwnBold    = regexp.MustCompile(`\*\*([^*]+)\*\*|__([^_]+)__`)
	mrkdwnStrike  = regexp.MustCompile(`~~([^~]+)~~`)
)

// slackTextEscaper neutralizes Slack's control characters. strings.Replacer
// scans the input once, so the & it emits for &amp; is never re-escaped —
// which is exactly the "& first" ordering the entity encoding requires.
var slackTextEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	"\x00", "",
)

func escapeSlackText(s string) string { return slackTextEscaper.Replace(s) }

func mrkdwnLine(line string) string {
	// Markdown and Slack spell block quotes the same way, so the leading ">"
	// run is markup we re-emit rather than content to escape.
	if quote := mrkdwnQuote.FindString(line); quote != "" {
		return quote + mrkdwnLine(line[len(quote):])
	}
	if m := mrkdwnHeading.FindStringSubmatch(line); m != nil {
		return m[1] + "*" + mrkdwnSpans(strings.TrimSpace(m[2]), true) + "*"
	}
	return mrkdwnSpans(line, false)
}

// mrkdwnSpans splits a line on backticks: odd-indexed segments sit between a
// matched pair and keep their content, while a trailing odd segment has no
// closing backtick and is ordinary text.
func mrkdwnSpans(line string, inHeading bool) string {
	parts := strings.Split(line, "`")
	for i, part := range parts {
		if i%2 == 1 && i < len(parts)-1 {
			parts[i] = escapeSlackText(part)
			continue
		}
		parts[i] = mrkdwnSegment(part, inHeading)
	}
	return strings.Join(parts, "`")
}

// mrkdwnSegment rebuilds one non-code segment: literal runs are escaped, and
// the <url|label> links are appended as markup around escaped components.
func mrkdwnSegment(s string, inHeading bool) string {
	var b strings.Builder
	last := 0
	for _, m := range mrkdwnLink.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(mrkdwnInline(s[last:m[0]], inHeading))
		b.WriteString("<")
		b.WriteString(escapeSlackText(s[m[4]:m[5]]))
		if label := strings.TrimSpace(s[m[2]:m[3]]); label != "" {
			b.WriteString("|")
			b.WriteString(escapeSlackText(label))
		}
		b.WriteString(">")
		last = m[1]
	}
	b.WriteString(mrkdwnInline(s[last:], inHeading))
	return b.String()
}

func mrkdwnInline(s string, inHeading bool) string {
	s = escapeSlackText(s)
	s = mrkdwnStrike.ReplaceAllString(s, "~$1~")
	return mrkdwnBold.ReplaceAllStringFunc(s, func(m string) string {
		inner := strings.TrimSuffix(strings.TrimPrefix(m, "**"), "**")
		inner = strings.TrimSuffix(strings.TrimPrefix(inner, "__"), "__")
		if inHeading {
			// A heading is already wrapped in bold markers; nesting a second
			// pair only leaks stray asterisks into the rendered line.
			return inner
		}
		return "*" + inner + "*"
	})
}

// slackTextLimit is the character cap Slack enforces on a single message's
// text; chat.update rejects anything past it with msg_too_long. Held under the
// documented 4000 so the guard has room to be slightly wrong about how Slack
// counts a rare code point.
const slackTextLimit = 3900

// slackFitMarker opens a preview whose head was dropped, matching how the
// bridge marks a preview it has already tailed.
const slackFitMarker = "…"

// FitSlackProgress shortens progress text until Slack's mrkdwn rendering of it
// fits inside slackTextLimit. The bridge budgets a preview in runes of the
// agent's raw output, but escapeSlackText expands every & < > into a four- or
// five-character entity on the way out, so a preview well inside that budget
// as written still arrives over the cap: 3200 runes of shell output and diffs
// render as roughly 5000. Shortening works on the source and re-renders, since
// cutting the rendered string would leave a half-written &amp; or <url|label>
// on screen.
//
// The first line survives — it is the phase banner, and a reader seeing only
// part of the preview still needs to know which phase produced it. The body
// loses its head, matching the preview's own tail-keeping rule: the tail is
// where the agent currently is.
func FitSlackProgress(text string) string {
	if slackRenderedLen(text) <= slackTextLimit {
		return text
	}
	banner, body, _ := strings.Cut(text, "\n")
	if slackRenderedLen(joinSlackProgress(banner, "")) > slackTextLimit {
		// A single overlong line: there is no banner worth protecting.
		banner, body = "", text
	}
	runes := []rune(body)
	// Runes to drop from the body's head. Rendering is monotone in the cut, so
	// the first cut that fits is the one that keeps the most text.
	drop := sort.Search(len(runes), func(i int) bool {
		return slackRenderedLen(joinSlackProgress(banner, string(runes[i:]))) <= slackTextLimit
	})
	return joinSlackProgress(banner, string(runes[drop:]))
}

func slackRenderedLen(text string) int { return len([]rune(SlackMrkdwn(text))) }

// joinSlackProgress reassembles a banner and a head-trimmed body, restoring the
// blank line the bridge puts between them.
func joinSlackProgress(banner, body string) string {
	body = strings.TrimLeft(body, "\n")
	switch {
	case banner == "" && body == "":
		return slackFitMarker
	case banner == "":
		return slackFitMarker + body
	case body == "":
		return banner
	default:
		return banner + "\n\n" + slackFitMarker + body
	}
}
