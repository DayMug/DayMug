package codexcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"

	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
)

type rolloutLine struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// readLatestCodexTotalUsage returns the last cumulative token counter written
// to a rollout before a resumed process starts. Rollouts are append-only, so
// scanning to the last valid token_count frame also tolerates a truncated
// final line left by an interrupted previous process.
func readLatestCodexTotalUsage(path string) (*codexTokenUsage, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var latest *codexTokenUsage
	sc := streamcommon.NewTranscriptScanner(f)
	for sc.Scan() {
		raw := sc.Bytes()
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var line rolloutLine
		if err := json.Unmarshal(raw, &line); err != nil || line.Type != "event_msg" {
			continue
		}
		var payload struct {
			Type string          `json:"type"`
			Info *codexTokenInfo `json:"info,omitempty"`
		}
		if err := json.Unmarshal(line.Payload, &payload); err != nil || payload.Type != "token_count" || payload.Info == nil || payload.Info.TotalTokenUsage == nil {
			continue
		}
		value := *payload.Info.TotalTokenUsage
		latest = &value
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return latest, nil
}
