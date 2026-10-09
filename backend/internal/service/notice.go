package service

import (
	"encoding/json"
	"log"
)

// Notice kinds the web UI renders from structured fields.
const (
	NoticeKindTaskGuardrail  = "task_guardrail"
	NoticeKindTransientRetry = "transient_retry"
)

// Guardrail trigger codes carried in Notice.Triggers.
const (
	GuardrailTriggerModelCalls = "model_calls"
	GuardrailTriggerToolCalls  = "tool_calls"
	GuardrailTriggerCost       = "cost"
)

// Notice is the structured form of a server-authored transcript notice. The
// persisted row's content keeps a ready-made sentence for IM threads (which
// have no locale) and for history written before this existed; the web UI
// renders from these fields instead so the notice follows the viewer's locale.
type Notice struct {
	Kind string `json:"kind"`

	// task_guardrail
	Triggers   []string `json:"triggers,omitempty"`
	ModelCalls int      `json:"model_calls,omitempty"`
	ToolCalls  int      `json:"tool_calls,omitempty"`
	// RecordedCostUSD is zero while the task has not reported its cost yet.
	RecordedCostUSD float64 `json:"recorded_cost_usd,omitempty"`

	// transient_retry
	DelaySeconds int `json:"delay_seconds,omitempty"`
	MaxAttempts  int `json:"max_attempts,omitempty"`
}

// noticeMetadata builds the message metadata for a notice row. Returns nil
// when there is nothing to record, so plain notices keep an empty column.
func noticeMetadata(noticeType string, notice *Notice) json.RawMessage {
	if noticeType == "" && notice == nil {
		return nil
	}
	raw, err := json.Marshal(struct {
		NoticeType string  `json:"notice_type,omitempty"`
		Notice     *Notice `json:"notice,omitempty"`
	}{noticeType, notice})
	if err != nil {
		log.Printf("[stream] marshal notice metadata: %v", err)
		return nil
	}
	return raw
}
