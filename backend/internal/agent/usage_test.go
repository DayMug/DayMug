package agent

import "testing"

func TestNewUsageEventKeepsReportAndJSONInStep(t *testing.T) {
	evt, err := NewUsageEvent(UsageReport{
		InputTokens: 1, OutputTokens: 2, TotalCostUSD: USD(0),
		PerModel:   map[string]ModelUsage{"m": {InputTokens: 1, OutputTokens: 2, PricingCacheReadInputTokens: 9}},
		Cumulative: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A reported zero cost stays on the wire; pricing-only counters and the
	// semantics flag never reach it.
	const want = `{"input_tokens":1,"output_tokens":2,"per_model":{"m":{"input_tokens":1,"output_tokens":2}},"total_cost_usd":0}`
	if evt.Kind != KindUsage || evt.Content != want {
		t.Fatalf("event = %s %s, want usage %s", evt.Kind, evt.Content, want)
	}
	report, ok := UsageOf(evt)
	if !ok || !report.Cumulative || report.PerModel["m"].PricingCacheReadInputTokens != 9 {
		t.Fatalf("UsageOf = %+v, %v; want the typed report back", report, ok)
	}
}

func TestUsageOfDecodesJSONOnlyEventsAsPerTurn(t *testing.T) {
	report, ok := UsageOf(StreamEvent{Kind: KindUsage, Content: `{"input_tokens":3,"total_cost_usd":0.5,"num_turns":2}`})
	if !ok || report.InputTokens != 3 || report.Cost() != 0.5 || report.NumTurns != 2 || report.Cumulative {
		t.Fatalf("UsageOf = %+v, %v", report, ok)
	}
	if _, ok := UsageOf(StreamEvent{Kind: KindDelta, Content: `{}`}); ok {
		t.Fatal("UsageOf accepted a non-usage event")
	}
	if _, ok := UsageOf(StreamEvent{Kind: KindUsage, Content: `not json`}); ok {
		t.Fatal("UsageOf accepted an unreadable payload")
	}
}
