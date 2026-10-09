package codexapp

import (
	"strconv"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/config"
)

// compatibleProviderID names the model provider DayMug defines for an
// openai-compatible account. Codex records the provider on every thread, so it
// must stay stable across releases or resumed threads would point at nothing.
const compatibleProviderID = "daymug"

// compatibleProviderArgs turns an openai-compatible account's API address and
// key into a Codex model provider, so an admin who only has an API key does
// not also have to hand-write config.toml in the account's CODEX_HOME.
//
// Both variables must be present: an account that leaves them empty (or keeps
// its key under another env_key) is one whose CODEX_HOME already declares a
// provider, and overriding that would break it. The first-party codex type is
// never touched — injecting a provider there would swap its ChatGPT login for
// an API key.
func compatibleProviderArgs(provider string, env map[string]string) []string {
	if provider != config.CLITypeOpenAICompatible {
		return nil
	}
	baseURL := strings.TrimSpace(env["OPENAI_BASE_URL"])
	if baseURL == "" || strings.TrimSpace(env["OPENAI_API_KEY"]) == "" {
		return nil
	}
	prefix := "model_providers." + compatibleProviderID + "."
	return []string{
		"-c", "model_provider=" + strconv.Quote(compatibleProviderID),
		"-c", prefix + "name=" + strconv.Quote("DayMug compatible API"),
		"-c", prefix + "base_url=" + strconv.Quote(baseURL),
		"-c", prefix + "env_key=" + strconv.Quote("OPENAI_API_KEY"),
		"-c", prefix + "wire_api=" + strconv.Quote("responses"),
	}
}
