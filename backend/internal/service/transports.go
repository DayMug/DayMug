package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/DayMug/DayMug/backend/internal/config"
)

// TransportsKey is the app_settings row holding the admin-selected transport
// of each provider type. Like the model registry it is one row for the whole
// map, so a save is one atomic write.
const TransportsKey = "agent.transports"

// Transport ids. A transport is how DayMug drives a provider's agent:
//   - cli: one `claude -p` / `codex exec` child per turn. Cannot accept input
//     while a turn runs, so the composer only offers "send after current".
//   - agent-sdk / app-server: a structured, long-lived session that accepts
//     steering input mid-turn and reports real context-window usage.
const (
	TransportCLI       = "cli"
	TransportAgentSDK  = "agent-sdk"
	TransportAppServer = "app-server"
)

// transportOptions lists, per provider type, the transports this binary can
// run. The first entry is the default. The compatible types have exactly one:
// claude-compatible relies on the CLI's cost substitution and openai-compatible
// on app-server's provider plumbing, neither of which the other transport has.
var transportOptions = map[string][]string{
	config.CLITypeClaude:           {TransportAgentSDK, TransportCLI},
	config.CLITypeCodex:            {TransportAppServer, TransportCLI},
	config.CLITypeClaudeCompatible: {TransportCLI},
	config.CLITypeOpenAICompatible: {TransportAppServer},
}

var (
	transportsMu sync.RWMutex
	transports   = map[string]string{}

	// transportsWriteMu serialises the whole merge → persist → swap sequence.
	// Without it two concurrent saves both merge onto the same snapshot and
	// the later write silently drops the other's change. Readers stay on
	// transportsMu so a slow DB write never blocks TransportFor.
	transportsWriteMu sync.Mutex
)

// TransportOptions returns the transports a provider type supports, default
// first. Nil for an unknown type.
func TransportOptions(provider string) []string {
	return append([]string(nil), transportOptions[provider]...)
}

// DefaultTransport is the transport a provider type runs on until an admin
// picks another.
func DefaultTransport(provider string) string {
	if opts := transportOptions[provider]; len(opts) > 0 {
		return opts[0]
	}
	return ""
}

// TransportFor returns the transport currently selected for a provider type.
func TransportFor(provider string) string {
	transportsMu.RLock()
	selected := transports[provider]
	transportsMu.RUnlock()
	if selected != "" {
		return selected
	}
	return DefaultTransport(provider)
}

// SetTransports atomically replaces the active selection. Exported for the
// save path and for tests.
func SetTransports(m map[string]string) {
	next := make(map[string]string, len(m))
	for k, v := range m {
		next[k] = v
	}
	transportsMu.Lock()
	transports = next
	transportsMu.Unlock()
}

// NormalizeTransports validates a selection and drops entries that equal
// their type's default, so "never configured" and "set back to default" are
// the same state on disk.
func NormalizeTransports(m map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(m))
	for provider, transport := range m {
		provider = strings.TrimSpace(provider)
		transport = strings.TrimSpace(transport)
		opts, ok := transportOptions[provider]
		if !ok {
			return nil, fmt.Errorf("unknown provider type %q", provider)
		}
		if transport == "" {
			continue
		}
		if !containsString(opts, transport) {
			return nil, fmt.Errorf("provider type %s does not support transport %q (supported: %s)",
				provider, transport, strings.Join(opts, ", "))
		}
		if transport != opts[0] {
			out[provider] = transport
		}
	}
	return out, nil
}

// LoadTransports reads the selection from app_settings. A missing row means
// every type runs on its default transport. An invalid stored value is
// reported and ignored rather than half-applied.
func LoadTransports(ctx context.Context, s AppSettingStore) error {
	if s == nil {
		return nil
	}
	transportsWriteMu.Lock()
	defer transportsWriteMu.Unlock()
	raw, err := s.GetAppSetting(ctx, TransportsKey)
	if err != nil {
		return fmt.Errorf("load transports: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		SetTransports(nil)
		return nil
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return fmt.Errorf("parse transports: %w", err)
	}
	normalised, err := NormalizeTransports(parsed)
	if err != nil {
		return fmt.Errorf("parse transports: %w", err)
	}
	SetTransports(normalised)
	return nil
}

// SaveTransports validates, persists and hot-swaps the selection. Types absent
// from m keep their current transport; the cache only changes once the write
// succeeds.
func SaveTransports(ctx context.Context, s AppSettingStore, m map[string]string) error {
	if s == nil {
		return Internal("app setting store unavailable", nil)
	}
	transportsWriteMu.Lock()
	defer transportsWriteMu.Unlock()
	transportsMu.RLock()
	merged := make(map[string]string, len(transports)+len(m))
	for k, v := range transports {
		merged[k] = v
	}
	transportsMu.RUnlock()
	for k, v := range m {
		merged[strings.TrimSpace(k)] = v
	}
	normalised, err := NormalizeTransports(merged)
	if err != nil {
		return BadRequest(err.Error())
	}
	value := ""
	if len(normalised) > 0 {
		buf, err := json.Marshal(normalised)
		if err != nil {
			return Internal(err.Error(), err)
		}
		value = string(buf)
	}
	if err := s.SetAppSetting(ctx, TransportsKey, value); err != nil {
		return Internal(err.Error(), err)
	}
	SetTransports(normalised)
	return nil
}

func containsString(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
