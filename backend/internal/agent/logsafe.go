package agent

import (
	"fmt"
	"strings"
)

// RedactedArgv returns a copy of argv fit for the server log. Spawn lines are
// logged on every turn and the log file outlives any conversation, so values
// that carry user- or operator-authored text (system prompts, personas) are
// replaced by their length: enough to debug argv shape, nothing to leak.
//
// valueFlags names flags whose following argument is sensitive
// ("--append-system-prompt <text>"); keyPrefixes matches arguments that embed
// the value after a key ("developer_instructions=<text>").
func RedactedArgv(argv []string, valueFlags, keyPrefixes []string) []string {
	out := make([]string, len(argv))
	copy(out, argv)
	for i := 0; i < len(out); i++ {
		for _, f := range valueFlags {
			if out[i] == f && i+1 < len(out) {
				out[i+1] = redactedLen(out[i+1])
				i++
				break
			}
		}
		if i >= len(out) {
			break
		}
		for _, p := range keyPrefixes {
			if strings.HasPrefix(out[i], p) {
				out[i] = p + redactedLen(out[i][len(p):])
				break
			}
		}
	}
	return out
}

// PromptLogValue summarizes a prompt for logging without its content.
func PromptLogValue(prompt string) string {
	return redactedLen(prompt)
}

func redactedLen(s string) string {
	return fmt.Sprintf("<redacted %d bytes>", len(s))
}
