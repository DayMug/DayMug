package codexcli

import (
	"slices"
	"testing"
)

func TestSystemPromptArgs_EmptyPromptAddsNothing(t *testing.T) {
	if got := systemPromptArgs(""); got != nil {
		t.Errorf("systemPromptArgs(\"\") = %#v, want nil", got)
	}
}

func TestSystemPromptArgs_BuildsConfigOverride(t *testing.T) {
	got := systemPromptArgs("## Your Identity\nbe terse")
	want := []string{"-c", `developer_instructions="## Your Identity\nbe terse"`}
	if !slices.Equal(got, want) {
		t.Errorf("systemPromptArgs() = %#v, want %#v", got, want)
	}
}

// TestTOMLBasicString covers the inputs that made the naive "pass the prompt
// raw" approach lossy. Codex parses a `-c key=value` value as TOML and only
// treats it as a literal when the parse fails, so each case below either
// silently dropped characters or changed type before quoting was added.
func TestTOMLBasicString(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "be terse", `"be terse"`},
		{"trailing quote", `ends with "x"`, `"ends with \"x\""`},
		{"fully quoted", `"whole thing"`, `"\"whole thing\""`},
		{"bare bool", "true", `"true"`},
		{"bare int", "123", `"123"`},
		{"array-looking", "[a, b]", `"[a, b]"`},
		{"backslash", `C:\path`, `"C:\\path"`},
		{"newline and tab", "one\n\ttwo", `"one\n\ttwo"`},
		{"carriage return", "one\r\ntwo", `"one\r\ntwo"`},
		{"control char", "bell\x07here", `"bell\u0007here"`},
		{"non-ascii stays raw", "中文 emoji 🎉", `"中文 emoji 🎉"`},
		{"marker line", "[DAYMUG_ARTIFACT path]", `"[DAYMUG_ARTIFACT path]"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tomlBasicString(tt.in); got != tt.want {
				t.Errorf("tomlBasicString(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}
