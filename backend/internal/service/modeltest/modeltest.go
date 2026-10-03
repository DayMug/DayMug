// Package modeltest holds the model registry fixture tests seed into
// service.ProviderModels. Production ships that baseline empty — every model
// comes from the admin-edited registry — but most tests exercise conversation
// flows that need some valid model without caring which, and configuring a
// registry row per test account would bury what each test is about.
//
// It deliberately does not import service, so service's own tests can use it.
package modeltest

// Models is the fixture baseline. The first entry of each list is what an
// unconfigured account treats as its default model.
func Models() map[string][]string {
	return map[string][]string{
		"claude":            {"claude-opus-5-5[1m]", "claude-opus-5[1m]", "claude-fable-5-1", "claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"},
		"codex":             {"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-6-astra"},
		"claude-compatible": {},
		"openai-compatible": {},
	}
}
