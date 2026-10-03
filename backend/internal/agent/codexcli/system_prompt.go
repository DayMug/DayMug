package codexcli

import (
	"fmt"
	"strings"
)

// developerInstructionsKey is codex's config key for extra instructions.
// Codex renders them as the first `developer` message of the prompt, ahead of
// its own skills / permissions / apps blocks — those are kept, and AGENTS.md
// is still read, so this is purely additive. It is codex's equivalent of
// claude's `--append-system-prompt`.
const developerInstructionsKey = "developer_instructions"

// systemPromptArgs renders the shared system prompt as a `-c` override, or nil
// when there is nothing to inject.
func systemPromptArgs(systemPrompt string) []string {
	if systemPrompt == "" {
		return nil
	}
	return []string{"-c", developerInstructionsKey + "=" + tomlBasicString(systemPrompt)}
}

// tomlBasicString quotes s as a TOML basic string.
//
// Encoding is mandatory, not cosmetic: codex parses a `-c key=value` value as
// TOML and only falls back to treating it as a literal when that parse fails,
// which mangles inputs an operator-authored persona really can produce. A
// prompt ending in `"` silently loses that quote, a fully quoted prompt loses
// both, and a prompt that is exactly `true` or `123` becomes a bool/int and
// makes codex reject the config outright. Quoting up front means every input
// parses as a string and round-trips byte-for-byte.
func tomlBasicString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			// TOML forbids raw control characters inside basic strings.
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
