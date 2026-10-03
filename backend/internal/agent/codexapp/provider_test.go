package codexapp

import (
	"slices"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
)

func TestCompatibleProviderArgs(t *testing.T) {
	full := map[string]string{"OPENAI_BASE_URL": " https://api.example.com/v1 ", "OPENAI_API_KEY": "sk-test"}
	tests := []struct {
		name     string
		provider string
		env      map[string]string
		want     bool
	}{
		{"compatible account with address and key", config.CLITypeOpenAICompatible, full, true},
		{"first-party codex keeps its login", config.CLITypeCodex, full, false},
		{"no address means config.toml owns the provider", config.CLITypeOpenAICompatible, map[string]string{"OPENAI_API_KEY": "sk"}, false},
		{"no key means another env_key is in use", config.CLITypeOpenAICompatible, map[string]string{"OPENAI_BASE_URL": "https://x"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compatibleProviderArgs(tt.provider, tt.env)
			if (got != nil) != tt.want {
				t.Fatalf("args = %q, want injected=%v", got, tt.want)
			}
		})
	}
	got := compatibleProviderArgs(config.CLITypeOpenAICompatible, full)
	for _, arg := range []string{
		`model_provider="daymug"`,
		`model_providers.daymug.base_url="https://api.example.com/v1"`,
		`model_providers.daymug.env_key="OPENAI_API_KEY"`,
		`model_providers.daymug.wire_api="responses"`,
	} {
		if !slices.Contains(got, arg) {
			t.Fatalf("args %q missing %q", got, arg)
		}
	}
	// The key itself must never reach argv, where ps would show it.
	if slices.ContainsFunc(got, func(a string) bool { return strings.Contains(a, "sk-test") }) {
		t.Fatalf("args leak the API key: %q", got)
	}
}
