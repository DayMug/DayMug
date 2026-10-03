// Package codexcommon translates Codex's own concepts — thread items (commands,
// file changes, MCP tool calls, web searches, prose) and token usage — into
// DayMug's event alphabet. Codex reaches DayMug over two
// transports (`codex exec --json` in codexcli, the app-server JSON-RPC in
// codexapp) whose wire formats differ only in spelling: snake_case vs
// camelCase, `turn.completed` vs `thread/tokenUsage/updated`. Each transport
// decodes its own wire shape into the neutral inputs below and hands the
// translation to this package, so a chat card or a bill cannot differ by which
// transport the operator happened to pick.
package codexcommon

import (
	"fmt"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/pricing"
	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
)

// ToolOutputLimit bounds one tool payload. A rejected patch can carry every
// diff it tried to apply and a command can print a whole file; the chat UI
// renders this inline, and the frame is persisted per message.
const ToolOutputLimit = 8 << 10

// Truncate caps s at ToolOutputLimit and says so, so a reader never mistakes a
// clipped output for the command's complete answer.
func Truncate(s string) string {
	if len(s) <= ToolOutputLimit {
		return s
	}
	return s[:ToolOutputLimit] + "\n… (truncated)"
}

// Command is one Codex command execution, already decoded from either wire.
type Command struct {
	ID               string
	Command          string
	AggregatedOutput string
	// Status is Codex's lifecycle word ("completed", "failed", …). Empty when
	// the wire did not say.
	Status   string
	ExitCode *int
}

// CommandStarted is the tool card for a command Codex has begun running. Every
// command is labelled "Bash" so the UI's shell-tool affordances apply without
// a Codex-specific rendering path.
func CommandStarted(c Command) []agent.StreamEvent {
	payload := map[string]any{"id": c.ID, "name": "Bash"}
	if c.Command != "" {
		payload["command"] = c.Command
	}
	return streamcommon.Event(agent.KindToolUseStart, payload)
}

// CommandCompleted is the tool result for a finished command. A command that
// printed nothing still gets a body naming its status and exit code: an empty
// result card reads as "the tool never answered".
func CommandCompleted(c Command) []agent.StreamEvent {
	exit := 0
	if c.ExitCode != nil {
		exit = *c.ExitCode
	}
	body := Truncate(strings.TrimSpace(c.AggregatedOutput))
	if body == "" {
		if c.Status != "" {
			body = fmt.Sprintf("(%s, exit %d)", c.Status, exit)
		} else {
			body = fmt.Sprintf("(exit %d)", exit)
		}
	}
	input := map[string]any{"output": body}
	if c.Command != "" {
		input["command"] = c.Command
	}
	return streamcommon.Event(agent.KindToolResult, map[string]any{
		"id": c.ID, "name": "Bash", "exit_code": exit, "content": body, "input": input,
	})
}

// TokenUsage is one Codex usage figure in Codex's own semantics: InputTokens is
// the GROSS prompt with the cached prefix folded in, and OutputTokens already
// includes ReasoningOutputTokens. It must already be the spend being reported
// (one turn or one model request), never a thread's running total — turning a
// cumulative counter into a delta is transport state, not translation.
type TokenUsage struct {
	InputTokens           int
	CachedInputTokens     int
	OutputTokens          int
	ReasoningOutputTokens int
	// TotalTokens is what currently occupies the context window.
	TotalTokens int
}

// UsageEvents turns one usage figure into a KindUsage frame and, when the
// context window is known (> 0), a KindContextUsage frame.
//
// input_tokens is emitted NET of the cache hit, to match Anthropic's semantics
// that the shared usage page and the DB rollup assume: Claude reports
// input_tokens and cache_read_input_tokens as disjoint, so "Input" means
// uncached input only. Surfacing Codex's gross figure made the same
// conversation look an order of magnitude heavier under Codex than under
// Claude.
//
// Cost and the per_model breakdown need model, which must be DayMug's model id
// from the RunRequest: Codex's own stream sometimes reports the bare upstream
// id (e.g. "gpt-5" for "gpt-5.5"), which does not match the price table.
// provider picks the price table — a compatible endpoint's model ids can
// collide with first-party ones while billing at entirely different rates.
// With no model the frame carries tokens only.
func UsageEvents(provider, model string, u TokenUsage, contextWindow int) []agent.StreamEvent {
	uncached := max(u.InputTokens-u.CachedInputTokens, 0)
	// Codex reports each model request's own spend, never a thread total, so
	// the report is not Cumulative: consumers add it up as it arrives.
	report := agent.UsageReport{
		InputTokens:           int64(uncached),
		OutputTokens:          int64(u.OutputTokens),
		CacheReadInputTokens:  int64(max(u.CachedInputTokens, 0)),
		ReasoningOutputTokens: int64(max(u.ReasoningOutputTokens, 0)),
	}
	if model != "" {
		// ComputeCodexCost wants the gross prompt and splits the cached prefix
		// out itself. Rebuilding it from the netted figure keeps the bill
		// consistent with the tokens recorded beside it even when a report
		// claims more cached than total input.
		cost := pricing.ComputeCodexCost(provider, model, uncached+u.CachedInputTokens, u.CachedInputTokens, u.OutputTokens)
		entry := agent.ModelUsage{
			InputTokens:           report.InputTokens,
			OutputTokens:          report.OutputTokens,
			CacheReadInputTokens:  report.CacheReadInputTokens,
			ReasoningOutputTokens: report.ReasoningOutputTokens,
		}
		if cost > 0 {
			report.TotalCostUSD = agent.USD(cost)
			entry.CostUSD = agent.USD(cost)
		}
		report.PerModel = map[string]agent.ModelUsage{model: entry}
	}
	events := streamcommon.UsageEvent(report)
	if contextWindow > 0 {
		// Codex defines total_tokens as the tokens currently occupying the
		// context window. input_tokens omits the response that will be carried
		// into the next turn, so using it delays context warnings and
		// compaction. Older payloads did not always include total_tokens;
		// retain their best available input-only estimate instead of zero.
		contextTokens := u.TotalTokens
		if contextTokens <= 0 {
			contextTokens = u.InputTokens
		}
		events = append(events, streamcommon.Event(agent.KindContextUsage, map[string]int{
			"used":         min(contextTokens, contextWindow),
			"total":        contextWindow,
			"input_tokens": uncached,
			"cache_read":   u.CachedInputTokens,
		})...)
	}
	return events
}
