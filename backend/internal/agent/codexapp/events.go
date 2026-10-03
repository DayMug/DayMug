package codexapp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/codexcommon"
)

func mapNotification(provider string, msg wireMessage, model string) ([]agent.StreamEvent, bool, error) {
	switch msg.Method {
	case "item/agentMessage/delta":
		var p struct {
			Delta string `json:"delta"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if p.Delta != "" {
			return []agent.StreamEvent{{Kind: agent.KindDelta, Content: p.Delta}}, false, nil
		}
	case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		var p struct {
			Delta string `json:"delta"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		if p.Delta != "" {
			return []agent.StreamEvent{{Kind: agent.KindThinkingDelta, Content: p.Delta}}, false, nil
		}
	case "item/started", "item/completed":
		return mapItem(msg.Method, msg.Params), false, nil
	case "thread/tokenUsage/updated":
		if events, ok := usageEvents(provider, msg.Params, model); ok {
			return events, false, nil
		}
	case "error":
		var p struct {
			Error struct {
				Message        string          `json:"message"`
				CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
			} `json:"error"`
			WillRetry bool `json:"willRetry"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		// codex narrates its own stream-reconnect attempts through this same
		// notification ("Reconnecting... 2/5", willRetry=true) and keeps
		// driving the turn. Failing here aborted a turn the server had not
		// given up on, and replaced the eventual real cause — which arrives as
		// a willRetry=false error and again on turn/completed — with a
		// progress message. willRetry is checked before the auth probe for the
		// same reason: a retryable 401 is codex's to refresh, and if the
		// refresh fails the terminal frame still carries Unauthorized.
		if p.WillRetry {
			if p.Error.Message != "" {
				return nil, false, fmt.Errorf("%w: %s", errStreamRetry, p.Error.Message)
			}
			return nil, false, nil
		}
		if isUnauthorized(p.Error.CodexErrorInfo) {
			return nil, false, fmt.Errorf("%w: %s", errAuthenticationRequired, p.Error.Message)
		}
		if p.Error.Message != "" {
			return nil, false, fmt.Errorf("codex CAS: %s", p.Error.Message)
		}
	case "turn/completed":
		var p struct {
			Turn struct {
				Status string `json:"status"`
				Error  *struct {
					Message        string          `json:"message"`
					CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
				} `json:"error"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		switch p.Turn.Status {
		case "completed":
			return nil, true, nil
		case "failed":
			if p.Turn.Error != nil {
				if isUnauthorized(p.Turn.Error.CodexErrorInfo) {
					return nil, true, fmt.Errorf("%w: %s", errAuthenticationRequired, p.Turn.Error.Message)
				}
				return nil, true, fmt.Errorf("codex CAS: %s", p.Turn.Error.Message)
			}
			return nil, true, fmt.Errorf("codex CAS turn failed")
		case "interrupted":
			return nil, true, errTurnInterrupted
		default:
			// The protocol's TurnStatus enum also carries "inProgress" and may
			// grow terminal states we don't know; reporting those as a clean
			// completion would silently truncate the turn.
			return nil, true, fmt.Errorf("codex CAS turn ended with unexpected status %q", p.Turn.Status)
		}
	}
	return nil, false, nil
}

func isUnauthorized(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return containsUnauthorized(value)
}

func containsUnauthorized(value any) bool {
	switch v := value.(type) {
	case string:
		return v == "Unauthorized"
	case map[string]any:
		if kind, _ := v["type"].(string); kind == "Unauthorized" {
			return true
		}
		if status, ok := v["httpStatusCode"].(float64); ok && status == 401 {
			return true
		}
		for key, child := range v {
			if key == "Unauthorized" || containsUnauthorized(child) {
				return true
			}
		}
	}
	return false
}

// item is the union of the ThreadItem variants this adapter understands.
// Unknown variants decode into it harmlessly and map to no event.
type item struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Text             string          `json:"text"`
	Command          string          `json:"command"`
	AggregatedOutput string          `json:"aggregatedOutput"`
	Status           string          `json:"status"`
	ExitCode         *int            `json:"exitCode"`
	Content          []string        `json:"content"`
	Summary          []string        `json:"summary"`
	Changes          []fileChange    `json:"changes"`
	Server           string          `json:"server"`
	Tool             string          `json:"tool"`
	Arguments        json.RawMessage `json:"arguments"`
	Result           json.RawMessage `json:"result"`
	Error            *struct {
		Message string `json:"message"`
	} `json:"error"`
	Query string `json:"query"`
}

type fileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Diff string `json:"diff"`
}

// mapItem decodes an app-server thread item and hands the translation to
// codexcommon, so the exec transport renders the same cards.
func mapItem(method string, raw json.RawMessage) []agent.StreamEvent {
	var p struct {
		Item item `json:"item"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	started := method == "item/started"
	if !started && method != "item/completed" {
		return nil
	}
	return codexcommon.ItemEvents(p.Item.common(), started)
}

// appItemKinds is the app-server's camelCase spelling of each variant.
var appItemKinds = map[string]codexcommon.ItemKind{
	"agentMessage":     codexcommon.ItemAgentMessage,
	"reasoning":        codexcommon.ItemReasoning,
	"commandExecution": codexcommon.ItemCommand,
	"fileChange":       codexcommon.ItemFileChange,
	"mcpToolCall":      codexcommon.ItemMcpToolCall,
	"webSearch":        codexcommon.ItemWebSearch,
}

func (i item) common() codexcommon.Item {
	out := codexcommon.Item{
		Kind: appItemKinds[i.Type], ID: i.ID, Status: i.Status, Text: i.Text,
		Command: i.Command, AggregatedOutput: i.AggregatedOutput, ExitCode: i.ExitCode,
		Server: i.Server, Tool: i.Tool, Arguments: i.Arguments, Result: i.Result,
		Query: i.Query,
	}
	if out.Kind == codexcommon.ItemReasoning {
		// Deltas already streamed the thinking pane for turns that stream.
		// Non-streaming reasoning (a resumed thread replaying its tail, a model
		// that only emits the completed item) would otherwise be lost.
		out.Text = strings.TrimSpace(strings.Join(append(append([]string(nil), i.Summary...), i.Content...), "\n"))
	}
	if i.Error != nil {
		out.ErrorMessage = i.Error.Message
	}
	for _, change := range i.Changes {
		out.Changes = append(out.Changes, codexcommon.FileChange{Path: change.Path, Kind: change.Kind})
	}
	return out
}

func usageEvents(provider string, raw json.RawMessage, model string) ([]agent.StreamEvent, bool) {
	var p struct {
		TokenUsage struct {
			Last struct {
				Input     int `json:"inputTokens"`
				Cached    int `json:"cachedInputTokens"`
				Output    int `json:"outputTokens"`
				Reasoning int `json:"reasoningOutputTokens"`
				Total     int `json:"totalTokens"`
			} `json:"last"`
			ModelContextWindow *int `json:"modelContextWindow"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return nil, false
	}
	last := p.TokenUsage.Last
	window := 0
	if p.TokenUsage.ModelContextWindow != nil {
		window = *p.TokenUsage.ModelContextWindow
	}
	// `last` is one model request's spend; acceptUsage has already rejected a
	// restatement of an older turn, so every frame that reaches here is billed.
	return codexcommon.UsageEvents(provider, model, codexcommon.TokenUsage{
		InputTokens: last.Input, CachedInputTokens: last.Cached, OutputTokens: last.Output,
		ReasoningOutputTokens: last.Reasoning, TotalTokens: last.Total,
	}, window), true
}
