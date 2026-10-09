package codexapp

import (
	"encoding/json"
	"fmt"
	"testing"
)

// usageParams builds the shape codex actually sends. The numbers mirror a real
// rollout: `total` is the thread's running counter and `last` is the spend of
// one model request, so a multi-request turn reports several times and the
// consumer sums them.
func usageParams(turnID string, cumulative, lastInput, lastOutput int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(
		`{"threadId":"t","turnId":%q,"tokenUsage":{"total":{"totalTokens":%d},"last":{"inputTokens":%d,"outputTokens":%d,"totalTokens":%d}}}`,
		turnID, cumulative, lastInput, lastOutput, lastInput+lastOutput))
}

func newUsageServer() *accountServer {
	return &accountServer{threadTotals: make(map[string]int)}
}

func TestAcceptUsageKeepsEveryRequestOfATurn(t *testing.T) {
	srv := newUsageServer()

	// One turn, three model requests: 17992 → 46144 → 75232 cumulative input.
	// All three must be accepted, because each `last` is this turn's spend for
	// that request and dropping any of them under-bills the account.
	for i, p := range []json.RawMessage{
		usageParams("turn-1", 18376, 17992, 384),
		usageParams("turn-1", 46951, 28152, 423),
		usageParams("turn-1", 76264, 29088, 225),
	} {
		if !srv.acceptUsage("t", "turn-1", p) {
			t.Fatalf("report %d of the same turn was rejected", i+1)
		}
	}
}

func TestAcceptUsageRejectsAnEarlierTurnsNumbers(t *testing.T) {
	srv := newUsageServer()
	if !srv.acceptUsage("t", "turn-1", usageParams("turn-1", 76264, 29088, 225)) {
		t.Fatal("the first turn's report was rejected")
	}

	// turn-2 is interrupted before it spends anything. Codex restates turn-1's
	// figures, and the cumulative counter has not moved.
	if srv.acceptUsage("t", "turn-2", usageParams("turn-1", 76264, 29088, 225)) {
		t.Fatal("a report naming an earlier turn was accepted")
	}
}

func TestAcceptUsageRejectsAStalledCumulativeWithoutTurnID(t *testing.T) {
	srv := newUsageServer()
	if !srv.acceptUsage("t", "turn-1", usageParams("", 76264, 29088, 225)) {
		t.Fatal("the first turn's report was rejected")
	}
	// Older app-servers omit turnId, so the cumulative counter is the only
	// gate left: it has not advanced, therefore nothing was spent and `last`
	// must describe an earlier turn.
	if srv.acceptUsage("t", "turn-2", usageParams("", 76264, 29088, 225)) {
		t.Fatal("a stale report was accepted when turnId was unavailable")
	}
}

func TestAcceptUsageTrustsTheFirstReportOnAnUnseenThread(t *testing.T) {
	srv := newUsageServer()
	// A resume after the pool reclaimed the old server: this process has never
	// seen the thread, so its cumulative counter starts high and there is no
	// baseline to compare against. The first report may already contain this
	// turn's spend and must not be discarded.
	if !srv.acceptUsage("t", "", usageParams("", 500000, 1200, 40)) {
		t.Fatal("the first report on an unseen thread was rejected")
	}
}

func TestNoteUsageTotalNeverMovesBackwards(t *testing.T) {
	srv := newUsageServer()
	srv.noteUsageTotal("t", 76264)
	srv.noteUsageTotal("t", 100)
	if got := srv.usageBaseline("t"); got != 76264 {
		t.Fatalf("baseline = %d, want it to hold the high-water mark 76264", got)
	}
}

func TestAcceptUsageIgnoresUnparseableParams(t *testing.T) {
	srv := newUsageServer()
	// Not evidence of staleness — leave the decision to the mapper rather than
	// silently dropping a report we merely failed to read.
	if !srv.acceptUsage("t", "turn-1", json.RawMessage(`{`)) {
		t.Fatal("unparseable params must not be treated as stale")
	}
}
