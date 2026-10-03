package service

import (
	"regexp"
	"strings"
)

// These patterns are applied per single line for block markup and across the
// whole text for inline markup. They are deliberately simple — notification
// bodies are short and we only need to drop noise characters, not render a
// full CommonMark document.
var (
	mdHorizRule  = regexp.MustCompile(`^\s{0,3}(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$`)
	mdHeading    = regexp.MustCompile(`^\s{0,3}#{1,6}\s+`)
	mdBlockquote = regexp.MustCompile(`^\s{0,3}>\s?`)
	mdListMarker = regexp.MustCompile(`^(\s*)(?:[-*+]|\d+[.)])\s+`)
	mdImage      = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	mdLink       = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	mdBoldAst    = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdBoldUnd    = regexp.MustCompile(`__([^_]+)__`)
	mdItalicAst  = regexp.MustCompile(`\*([^*]+)\*`)
	mdItalicUnd  = regexp.MustCompile(`_([^_]+)_`)
	mdStrike     = regexp.MustCompile(`~~([^~]+)~~`)
	mdInlineCode = regexp.MustCompile("`([^`]+)`")
	mdBlankLines = regexp.MustCompile(`\n{3,}`)
)

// stripMarkdown converts a markdown string into readable plain text suitable
// for a push-notification body. It strips structural markup — headings, list
// bullets, blockquote markers, fenced/inline code delimiters, emphasis, and
// link/image syntax — while preserving the human-readable text those markers
// wrap. The result is trimmed of surrounding whitespace.
func stripMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Toggle fenced code blocks; drop the fence delimiter lines but keep
		// the code inside them as plain text.
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}
		// Horizontal rules carry no text content.
		if mdHorizRule.MatchString(line) {
			continue
		}
		line = mdHeading.ReplaceAllString(line, "")
		line = mdBlockquote.ReplaceAllString(line, "")
		line = mdListMarker.ReplaceAllString(line, "$1")
		out = append(out, line)
	}
	text := strings.Join(out, "\n")

	// Inline markup. Bold runs before italic so "**x**" is unwrapped before the
	// single-delimiter pass sees its asterisks.
	text = mdImage.ReplaceAllString(text, "$1")
	text = mdLink.ReplaceAllString(text, "$1")
	text = mdBoldAst.ReplaceAllString(text, "$1")
	text = mdBoldUnd.ReplaceAllString(text, "$1")
	text = mdItalicAst.ReplaceAllString(text, "$1")
	text = mdItalicUnd.ReplaceAllString(text, "$1")
	text = mdStrike.ReplaceAllString(text, "$1")
	text = mdInlineCode.ReplaceAllString(text, "$1")

	// Block removal can leave runs of blank lines behind.
	text = mdBlankLines.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}
