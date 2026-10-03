package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/prompts"
)

// TitleGenerator produces a short conversation title from transcript
// text. Implementations should be quick and side-effect free; a
// generator that fails (network, missing CLI, model error) returns
// ("", err) and the caller decides whether to fall back or retry
// later.
//
// account names the credentials the underlying CLI should authenticate
// against. Without it, every conversation's title generation hit the
// process-default config dir, which both ignored per-user provider
// bindings and silently funneled all title traffic onto a single
// account — tripping its rate limit and breaking titles globally.
type TitleGenerator interface {
	GenerateTitle(ctx context.Context, userPrompts []string, account TitleAccount) (string, error)
}

// TitleBackend is implemented by backends whose RunOneshot is too heavy to
// spend on an eight-word title. A Claude one-shot loads every built-in tool,
// MCP server schema and the CLAUDE.md-bearing system prompt (~69k input tokens
// measured); the purpose-built invocation drops all of that (~1k). Backends
// that don't implement it are summarized through RunOneshot.
type TitleBackend interface {
	TitleGenerator(model string) TitleGenerator
}

// TitleGeneratorOf returns b's purpose-built title generator, resolving a
// TransportSwitch to the transport selected right now, so the title runs on
// the same runtime chat does. ok is false when b should be summarized through
// RunOneshot instead.
func TitleGeneratorOf(b Backend, model string) (g TitleGenerator, ok bool) {
	tb, ok := Resolve(b).(TitleBackend)
	if !ok {
		return nil, false
	}
	return tb.TitleGenerator(model), true
}

// TitleAccount carries the per-conversation account binding the title
// generator should run under, mirroring the relevant subset of
// RunRequest the main runner reads. ConfigDir maps to
// CLAUDE_CONFIG_DIR (Claude family) or CODEX_HOME (Codex family); Env carries extra
// YAML-declared env vars (e.g. ANTHROPIC_API_KEY for API-key accounts).
//
// A zero value is legal and means "no account override": the
// generator then inherits whatever env the daymug process was started
// with. Tests and legacy callers that don't resolve an account rely
// on this.
type TitleAccount struct {
	ConfigDir string
	Env       map[string]string
}

// TitleMaxRunes caps the rune length of a finalised title so a
// chatty model can't produce a 500-rune blob that breaks the UI.
const TitleMaxRunes = 50

// TitlePromptMaxRunes caps how many runes of each user message we
// feed the title model. A 30-char title only needs the gist of the
// conversation opener — when users paste long code/log/document
// blobs as their first message the full content used to be re-sent
// to the cheap model on every retry until a title was produced,
// burning tokens with no quality benefit. The truncation marker
// keeps the model honest about the message being clipped.
const TitlePromptMaxRunes = 600

// titlePromptTruncMarker is appended to a message that was clipped
// by TitlePromptMaxRunes so the model can tell the input is partial.
const titlePromptTruncMarker = "…(truncated)"

// TitleAbstainSentinel is the literal string the prompt instructs
// the model to emit when the conversation is too thin to summarize.
// CleanTitle treats it as an empty title so the caller can retry on
// the next user/assistant exchange.
const TitleAbstainSentinel = prompts.TitleAbstain

// TitleSystemPrompt is the system-message form of the title prompt
// (claude's separate `--system-prompt` flag); TitleGenPromptTemplate
// is the combined instruction+transcript form for single-prompt
// backends (codex), with %s replaced by BuildTitleGenPrompt. Both are
// built from one shared instruction body in internal/prompts, so
// title quality stays comparable across providers by construction.
const (
	TitleSystemPrompt      = prompts.TitleSystem
	TitleGenPromptTemplate = prompts.TitleGenTemplate
)

// BuildTitleTranscript joins the per-message user prompts into the
// numbered transcript format the title pipeline expects. Each
// message is trimmed and clipped to TitlePromptMaxRunes.
func BuildTitleTranscript(userPrompts []string) (string, error) {
	if len(userPrompts) == 0 {
		return "", fmt.Errorf("title gen: no user prompts")
	}
	var b strings.Builder
	for i, p := range userPrompts {
		fmt.Fprintf(&b, "[Message %d] %s\n", i+1, truncateForTitle(strings.TrimSpace(p)))
	}
	return b.String(), nil
}

// BuildTitleGenPrompt wraps the transcript with the instruction
// template. Both backends use it: codex because it only accepts a
// single prompt, claude because system-prompt-only instructions
// weren't reliable — the model would treat the raw transcript as a
// chat turn and answer it after the title. Repeating the instruction
// in the user message collapses the response to a single line.
func BuildTitleGenPrompt(userPrompts []string) (string, error) {
	transcript, err := BuildTitleTranscript(userPrompts)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(TitleGenPromptTemplate, transcript), nil
}

// truncateForTitle clips a single user message to TitlePromptMaxRunes
// runes (rune-safe so CJK content doesn't get cut mid-codepoint) and
// appends titlePromptTruncMarker. Short messages pass through unchanged.
func truncateForTitle(s string) string {
	runes := []rune(s)
	if len(runes) <= TitlePromptMaxRunes {
		return s
	}
	return string(runes[:TitlePromptMaxRunes]) + titlePromptTruncMarker
}

// CleanTitle trims wrapping quotes and whitespace and caps the title
// at TitleMaxRunes runes. It also collapses to the first non-empty
// line so a chatty model that adds a trailing explanation can't
// pollute the title.
func CleanTitle(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			s = line
			break
		}
	}
	s = strings.Trim(s, "\"'`「」“”")
	s = strings.TrimSpace(s)
	if s == TitleAbstainSentinel {
		return ""
	}
	runes := []rune(s)
	if len(runes) > TitleMaxRunes {
		runes = runes[:TitleMaxRunes]
	}
	return string(runes)
}
