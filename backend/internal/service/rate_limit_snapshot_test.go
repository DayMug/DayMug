package service

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func decodeSnapshot(t *testing.T, frames []string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, f := range frames {
		var m map[string]any
		if err := json.Unmarshal([]byte(f), &m); err != nil {
			t.Fatalf("frame %q: %v", f, err)
		}
		out[m["type"].(string)] = m
	}
	return out
}

func TestPool_RateLimitSnapshotMergesStatuslessFrames(t *testing.T) {
	p := newTestPool(1, 0)
	now := time.Unix(1_000_000, 0)
	p.now = func() time.Time { return now }
	reset := now.Add(2 * time.Hour).Unix()

	p.RecordRateLimit("default", fmt.Sprintf(`{"type":"five_hour","status":"allowed","resets_at":%d,"utilization":10}`, reset))
	now = now.Add(time.Minute)
	p.RecordRateLimit("default", fmt.Sprintf(`{"type":"five_hour","resets_at":%d,"utilization":20}`, reset))

	got := decodeSnapshot(t, p.RateLimitSnapshot("default"))
	w := got["five_hour"]
	if w["status"] != "allowed" || w["utilization"] != float64(20) {
		t.Fatalf("status-less frame should refresh usage and keep status, got %+v", w)
	}
	if w["observed_at"] != float64(now.Unix()) {
		t.Fatalf("observed_at = %v, want %d", w["observed_at"], now.Unix())
	}
	if len(p.RateLimitSnapshot("alt")) != 0 {
		t.Fatalf("another account must not see this account's windows")
	}
}

func TestPool_RateLimitSnapshotDropsStaleWindows(t *testing.T) {
	p := newTestPool(1, 0)
	now := time.Unix(1_000_000, 0)
	p.now = func() time.Time { return now }

	p.RecordRateLimit("default", fmt.Sprintf(`{"type":"five_hour","resets_at":%d}`, now.Add(time.Hour).Unix()))
	p.RecordRateLimit("default", fmt.Sprintf(`{"type":"seven_day","resets_at":%d}`, now.Add(72*time.Hour).Unix()))

	now = now.Add(time.Hour)
	got := decodeSnapshot(t, p.RateLimitSnapshot("default"))
	if _, ok := got["five_hour"]; ok {
		t.Fatalf("a window past its reset must be dropped")
	}
	if _, ok := got["seven_day"]; !ok {
		t.Fatalf("seven_day should still be replayed")
	}

	now = now.Add(rateLimitSnapshotTTL)
	if frames := p.RateLimitSnapshot("default"); len(frames) != 0 {
		t.Fatalf("readings older than the TTL must be dropped, got %v", frames)
	}
}

func TestPool_RateLimitEmptyAccountUsesDefaultProvider(t *testing.T) {
	p := newTestPool(1, 0)
	p.RecordRateLimit("", fmt.Sprintf(`{"type":"five_hour","resets_at":%d}`, time.Now().Add(time.Hour).Unix()))
	if len(p.RateLimitSnapshot("default")) != 1 {
		t.Fatalf("an unnamed account should record against the default provider")
	}
}
