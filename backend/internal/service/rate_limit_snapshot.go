package service

import (
	"encoding/json"
	"sort"
	"time"
)

// rateLimitSnapshotTTL bounds how long a remembered plan-window reading is
// replayed. After five hours the 5h quota has certainly rolled over, so the
// percentage would describe a period that no longer exists.
const rateLimitSnapshotTTL = 5 * time.Hour

// rateLimitReading is the latest merged rate_limit payload for one window of
// one account, plus when the server saw it.
type rateLimitReading struct {
	payload    map[string]any
	observedAt time.Time
}

// RecordRateLimit remembers a rate_limit frame against the account it was
// reported for, so a browser that opens any conversation on that account —
// a new tab, another device — can see the quota before its own first turn.
// Memory only: a restart forgets it until the next turn reports again.
//
// Merging mirrors the chat UI: a frame with a status is claude's full
// headline payload and replaces the window; a status-less frame (codex, or
// claude's secondary windows) only refreshes usage and reset time, keeping
// the status the window last reported.
func (p *Pool) RecordRateLimit(accountName, content string) {
	if p == nil || content == "" {
		return
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return
	}
	window, _ := payload["type"].(string)
	if window == "" {
		return
	}
	accountName = p.rateLimitAccount(accountName)
	if accountName == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rateLimits == nil {
		p.rateLimits = make(map[string]map[string]rateLimitReading)
	}
	windows := p.rateLimits[accountName]
	if windows == nil {
		windows = make(map[string]rateLimitReading)
		p.rateLimits[accountName] = windows
	}
	if _, hasStatus := payload["status"]; !hasStatus {
		if prev, ok := windows[window]; ok {
			merged := make(map[string]any, len(prev.payload)+len(payload))
			for k, v := range prev.payload {
				merged[k] = v
			}
			for k, v := range payload {
				merged[k] = v
			}
			payload = merged
		}
	}
	windows[window] = rateLimitReading{payload: payload, observedAt: p.now()}
}

// RateLimitSnapshot returns the account's still-current rate_limit payloads,
// each stamped with observed_at (Unix seconds) so the UI can tell how old the
// reading is. Windows past their reset or older than the TTL are dropped.
func (p *Pool) RateLimitSnapshot(accountName string) []string {
	if p == nil {
		return nil
	}
	accountName = p.rateLimitAccount(accountName)
	if accountName == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	windows := p.rateLimits[accountName]
	names := make([]string, 0, len(windows))
	for name := range windows {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		reading := windows[name]
		resetsAt, _ := reading.payload["resets_at"].(float64)
		if now.Sub(reading.observedAt) >= rateLimitSnapshotTTL || now.Unix() >= int64(resetsAt) {
			delete(windows, name)
			continue
		}
		payload := make(map[string]any, len(reading.payload)+1)
		for k, v := range reading.payload {
			payload[k] = v
		}
		payload["observed_at"] = reading.observedAt.Unix()
		data, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		out = append(out, string(data))
	}
	return out
}

// rateLimitAccount resolves an empty account name to the default provider,
// the same fallback Cooldown uses for single-account setups.
func (p *Pool) rateLimitAccount(accountName string) string {
	if accountName != "" || p.cfg == nil {
		return accountName
	}
	provider, ok := p.cfg.DefaultProvider()
	if !ok {
		return ""
	}
	return provider.Name
}
