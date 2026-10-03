package agent

import (
	"encoding/json"
	"fmt"
)

// UsageReport is the typed payload of a KindUsage event. Adapters build it,
// NewUsageEvent serialises it into StreamEvent.Content — the JSON that rides
// on the assistant row's metadata and the `result` frame — and consumers read
// it back with UsageOf instead of each declaring its own anonymous struct.
//
// Field order is alphabetical by JSON key on purpose: the adapters used to
// marshal map[string]any, which encoding/json emits in sorted key order, and
// keeping the struct in that order keeps the persisted metadata byte-identical
// across the change. omitempty mirrors which keys each adapter used to leave
// out; the pointer fields are the ones some adapter emits even when zero.
type UsageReport struct {
	// CacheCreation is Anthropic's 5m/1h split of CacheCreationInputTokens,
	// present only when the provider reported a non-zero split.
	CacheCreation            *CacheCreationSplit `json:"cache_creation,omitempty"`
	CacheCreationInputTokens int64               `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int64               `json:"cache_read_input_tokens,omitempty"`
	// InputTokens is uncached input only, disjoint from the cache counters,
	// for every provider (Codex's gross figure is netted by its adapter).
	InputTokens int64 `json:"input_tokens"`
	// NumTurns is the number of model requests the provider says this report
	// covers; zero when it does not say.
	NumTurns              int64                 `json:"num_turns,omitempty"`
	OutputTokens          int64                 `json:"output_tokens"`
	PerModel              map[string]ModelUsage `json:"per_model,omitempty"`
	ReasoningOutputTokens int64                 `json:"reasoning_output_tokens,omitempty"`
	TotalCostUSD          *float64              `json:"total_cost_usd,omitempty"`

	// Cumulative declares that PerModel and TotalCostUSD are running totals
	// for the provider session rather than this report's own spend, so the
	// consumer must difference them against the previous report to bill. The
	// top-level token and cache counters are this report's own in both modes.
	// It is process-local: the wire JSON never carried it, and adding it
	// there would change every persisted assistant row.
	Cumulative bool `json:"-"`
}

// ModelUsage is one model's share of a UsageReport. Under a Cumulative report
// its counters and cost are session running totals.
type ModelUsage struct {
	CacheReadInputTokens  int64    `json:"cache_read_input_tokens,omitempty"`
	CostUSD               *float64 `json:"cost_usd,omitempty"`
	InputTokens           int64    `json:"input_tokens"`
	OutputTokens          int64    `json:"output_tokens"`
	ReasoningOutputTokens int64    `json:"reasoning_output_tokens,omitempty"`

	// PricingCacheReadInputTokens and PricingCacheCreationInputTokens are
	// cache counters an adapter needs to price this entry locally but that
	// were never on the wire — Claude's per-model modelUsage cache totals,
	// which claude-compatible prices against its own table. Keeping them off
	// the wire leaves persisted rows unchanged and keeps
	// cache_read_input_tokens meaning one thing across providers.
	PricingCacheReadInputTokens     int64 `json:"-"`
	PricingCacheCreationInputTokens int64 `json:"-"`
}

// CacheCreationSplit breaks cache writes down by TTL bucket; the 1h bucket is
// priced at roughly twice the 5m one.
type CacheCreationSplit struct {
	Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens,omitempty"`
	Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens,omitempty"`
}

// Cost returns TotalCostUSD, zero when the provider reported none.
func (r UsageReport) Cost() float64 {
	if r.TotalCostUSD == nil {
		return 0
	}
	return *r.TotalCostUSD
}

// Cost returns CostUSD, zero when none was reported.
func (m ModelUsage) Cost() float64 {
	if m.CostUSD == nil {
		return 0
	}
	return *m.CostUSD
}

// USD boxes a cost for the optional cost fields.
func USD(v float64) *float64 { return &v }

// NewUsageEvent is the only way adapters should build a KindUsage event: it
// keeps the typed report and its wire JSON in step.
func NewUsageEvent(r UsageReport) (StreamEvent, error) {
	return StreamEvent{Kind: KindUsage}.WithUsage(r)
}

// WithUsage replaces evt's usage report, re-serialising Content. Enrichment
// steps use it so the JSON and the typed report can never disagree.
func (evt StreamEvent) WithUsage(r UsageReport) (StreamEvent, error) {
	content, err := json.Marshal(r)
	if err != nil {
		return evt, fmt.Errorf("marshal usage report: %w", err)
	}
	evt.Content = string(content)
	evt.Usage = &r
	return evt, nil
}

// UsageOf returns the report a KindUsage event carries. Events built with
// NewUsageEvent return their typed report; an event that only has JSON (a
// replayed recording, a hand-built test frame) is decoded, and then
// Cumulative is false because the JSON never carried it.
func UsageOf(evt StreamEvent) (UsageReport, bool) {
	if evt.Kind != KindUsage {
		return UsageReport{}, false
	}
	if evt.Usage != nil {
		return *evt.Usage, true
	}
	var r UsageReport
	if evt.Content == "" || json.Unmarshal([]byte(evt.Content), &r) != nil {
		return UsageReport{}, false
	}
	return r, true
}
