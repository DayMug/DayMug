package service

import (
	"encoding/json"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// AppendSystem appends a non-blank fragment to opts.SystemPrompt, inserting a
// blank-line separator when the prompt already has content. It is the single
// append helper for every system-prompt assembly path (web, IM, oneshot).
func AppendSystem(opts *agent.RunRequest, fragment string) {
	if opts == nil || strings.TrimSpace(fragment) == "" {
		return
	}
	if opts.SystemPrompt != "" {
		opts.SystemPrompt += "\n\n"
	}
	opts.SystemPrompt += fragment
}

// ParseSessionID pulls the session id out of a system_init frame payload,
// returning "" when the frame isn't JSON or carries no session id.
func ParseSessionID(content string) string {
	var m struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(content), &m) == nil {
		return m.SessionID
	}
	return ""
}
