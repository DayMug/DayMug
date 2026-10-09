package codexcommon

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
)

// ItemKind names a Codex thread-item variant independent of its wire spelling
// (`command_execution` on `codex exec --json`, `commandExecution` on the
// app-server).
type ItemKind int

const (
	// ItemUnknown is every variant DayMug does not render; it maps to no event.
	ItemUnknown ItemKind = iota
	ItemAgentMessage
	ItemReasoning
	ItemCommand
	ItemFileChange
	ItemMcpToolCall
	ItemWebSearch
)

// FileChange is one path touched by a patch.
type FileChange struct {
	Path string
	// Kind is Codex's change word: "add", "delete" or "update".
	Kind string
}

// Item is one Codex thread item, already decoded from either wire. Only the
// fields meaningful for Kind are read.
type Item struct {
	Kind   ItemKind
	ID     string
	Status string

	// Text is the final prose of an agent message or reasoning item. Each
	// transport assembles it (the app-server splits reasoning into summary and
	// content parts) before handing it over.
	Text string

	// Command execution.
	Command          string
	AggregatedOutput string
	ExitCode         *int

	// File change.
	Changes []FileChange

	// MCP tool call. Arguments and Result are the raw JSON Codex sent; the
	// result is shown verbatim because its shape is the MCP server's, not
	// Codex's.
	Server       string
	Tool         string
	Arguments    json.RawMessage
	Result       json.RawMessage
	ErrorMessage string

	// Web search.
	Query string
}

// ItemEvents translates one item lifecycle notification into DayMug's tool
// alphabet. Every variant Codex can run as a side effect belongs here: mapping
// only commands meant a turn that edited files, called an MCP tool, or searched
// the web streamed prose with no visible activity at all, so the UI showed the
// model idling while it worked. Prose items surface only on completion; their
// incremental text arrives through the transports' own delta events.
func ItemEvents(it Item, started bool) []agent.StreamEvent {
	switch it.Kind {
	case ItemCommand:
		cmd := Command{
			ID: it.ID, Command: it.Command, AggregatedOutput: it.AggregatedOutput,
			Status: it.Status, ExitCode: it.ExitCode,
		}
		if started {
			return CommandStarted(cmd)
		}
		return CommandCompleted(cmd)
	case ItemFileChange:
		if started {
			return streamcommon.Event(agent.KindToolUseStart, map[string]any{
				"id": it.ID, "name": "ApplyPatch", "input": map[string]any{"files": changedPaths(it.Changes)},
			})
		}
		return fileChangeResult(it)
	case ItemMcpToolCall:
		name := mcpToolName(it)
		if started {
			return streamcommon.Event(agent.KindToolUseStart, map[string]any{
				"id": it.ID, "name": name, "input": mcpArguments(it),
			})
		}
		return mcpResult(it, name)
	case ItemWebSearch:
		if started {
			return streamcommon.Event(agent.KindToolUseStart, map[string]any{
				"id": it.ID, "name": "WebSearch", "input": map[string]any{"query": it.Query},
			})
		}
		return streamcommon.Event(agent.KindToolResult, map[string]any{
			"id": it.ID, "name": "WebSearch", "content": it.Query,
			"input": map[string]any{"query": it.Query},
		})
	case ItemAgentMessage:
		if !started && it.Text != "" {
			return []agent.StreamEvent{{Kind: agent.KindResult, Content: it.Text}}
		}
	case ItemReasoning:
		if !started && it.Text != "" {
			return []agent.StreamEvent{{Kind: agent.KindThinkingDelta, Content: it.Text}}
		}
	}
	return nil
}

func fileChangeResult(it Item) []agent.StreamEvent {
	summary := make([]string, 0, len(it.Changes))
	for _, change := range it.Changes {
		summary = append(summary, fmt.Sprintf("%s %s", change.Kind, change.Path))
	}
	body := Truncate(strings.Join(summary, "\n"))
	if body == "" {
		body = fmt.Sprintf("(%s)", it.Status)
	}
	return streamcommon.Event(agent.KindToolResult, map[string]any{
		"id": it.ID, "name": "ApplyPatch", "content": body,
		"input": map[string]any{"files": changedPaths(it.Changes), "status": it.Status},
	})
}

func mcpResult(it Item, name string) []agent.StreamEvent {
	body := ""
	if it.ErrorMessage != "" {
		body = it.ErrorMessage
	} else if len(it.Result) > 0 {
		body = string(it.Result)
	}
	body = Truncate(strings.TrimSpace(body))
	if body == "" {
		body = fmt.Sprintf("(%s)", it.Status)
	}
	return streamcommon.Event(agent.KindToolResult, map[string]any{
		"id": it.ID, "name": name, "content": body,
		"input": mcpArguments(it),
	})
}

// mcpToolName follows Claude's mcp__<server>__<tool> convention so a
// conversation's tool chips read the same whichever provider produced them.
func mcpToolName(it Item) string {
	switch {
	case it.Server != "" && it.Tool != "":
		return "mcp__" + it.Server + "__" + it.Tool
	case it.Tool != "":
		return it.Tool
	default:
		return "McpToolCall"
	}
}

func mcpArguments(it Item) map[string]any {
	input := map[string]any{"server": it.Server, "tool": it.Tool}
	if len(it.Arguments) > 0 && string(it.Arguments) != "null" {
		var decoded any
		if json.Unmarshal(it.Arguments, &decoded) == nil {
			input["arguments"] = decoded
		}
	}
	return input
}

func changedPaths(changes []FileChange) []string {
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		if change.Path != "" {
			paths = append(paths, change.Path)
		}
	}
	return paths
}
