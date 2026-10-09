package agent

import (
	"strings"
	"testing"
)

// TestCleanTitle pins down every trimming / truncation rule the title
// pipeline relies on. The model is asked for "no quotes, no prefix"
// but routinely returns "Title: foo\n— explanation" or '"foo"' — so
// the rules below are belt-and-suspenders against that drift.
func TestCleanTitle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Hello World", "Hello World"},
		{"trim spaces", "  Hello  ", "Hello"},
		{"strip double quotes", `"Hello World"`, "Hello World"},
		{"strip single quotes", "'foo bar'", "foo bar"},
		{"strip cjk quotes", "「项目重构」", "项目重构"},
		{"chinese kept", "项目重构方案", "项目重构方案"},
		{"truncate runes", strings.Repeat("中", 60), strings.Repeat("中", 50)},
		{"first nonempty line wins", "\n\nTitle text\nExplanation: blah blah", "Title text"},
		{"empty in -> empty out", "", ""},
		{"whitespace only", "   \n  \n  ", ""},
		{"abstain sentinel becomes empty", "SKIP", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanTitle(tt.in)
			if got != tt.want {
				t.Errorf("CleanTitle(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestBuildTitleGenPrompt_NoPrompts guards the empty-input contract.
// We want callers to get a clear "no user prompts" error rather than
// the model hallucinating a title from the bare template.
func TestBuildTitleGenPrompt_NoPrompts(t *testing.T) {
	if _, err := BuildTitleGenPrompt(nil); err == nil {
		t.Error("expected error for nil userPrompts, got nil")
	}
	if _, err := BuildTitleGenPrompt([]string{}); err == nil {
		t.Error("expected error for empty userPrompts, got nil")
	}
}

// TestBuildTitleGenPrompt_NumbersAndTrimsMessages confirms the
// transcript format that's baked into the prompt template: each user
// message gets a [Message N] prefix and is trimmed of whitespace.
// Other layers depend on this shape (the prompt is what the model
// sees), so any change here is a behaviour change.
func TestBuildTitleGenPrompt_NumbersAndTrimsMessages(t *testing.T) {
	out, err := BuildTitleGenPrompt([]string{"  first  ", "second"})
	if err != nil {
		t.Fatalf("BuildTitleGenPrompt: %v", err)
	}
	if !strings.Contains(out, "[Message 1] first") {
		t.Errorf("first message missing or untrimmed: %q", out)
	}
	if !strings.Contains(out, "[Message 2] second") {
		t.Errorf("second message missing: %q", out)
	}
}

// TestBuildTitleGenPrompt_TruncatesLongMessages pins the per-message
// cap that prevents a pasted code/log blob from blowing up the cheap
// title-model bill on every retry. CJK content must be cut on a rune
// boundary (not a byte boundary) so we don't corrupt the input.
func TestBuildTitleGenPrompt_TruncatesLongMessages(t *testing.T) {
	long := strings.Repeat("a", TitlePromptMaxRunes+200)
	out, err := BuildTitleGenPrompt([]string{long})
	if err != nil {
		t.Fatalf("BuildTitleGenPrompt: %v", err)
	}
	wantBody := strings.Repeat("a", TitlePromptMaxRunes) + "…(truncated)"
	if !strings.Contains(out, "[Message 1] "+wantBody) {
		t.Errorf("long message not truncated as expected; got: %q", out)
	}
	if strings.Contains(out, strings.Repeat("a", TitlePromptMaxRunes+1)) {
		t.Errorf("body exceeded cap: %q", out)
	}

	cjk := strings.Repeat("中", TitlePromptMaxRunes+50)
	out, err = BuildTitleGenPrompt([]string{cjk})
	if err != nil {
		t.Fatalf("BuildTitleGenPrompt cjk: %v", err)
	}
	wantCJK := strings.Repeat("中", TitlePromptMaxRunes) + "…(truncated)"
	if !strings.Contains(out, "[Message 1] "+wantCJK) {
		t.Errorf("cjk message not truncated on rune boundary; got: %q", out)
	}
}

// TestBuildTitleGenPrompt_ShortMessagesPassThrough guards the
// no-op-on-small-input contract: titles for short conversations
// must not get a phantom truncation marker.
func TestBuildTitleGenPrompt_ShortMessagesPassThrough(t *testing.T) {
	out, err := BuildTitleGenPrompt([]string{"hi"})
	if err != nil {
		t.Fatalf("BuildTitleGenPrompt: %v", err)
	}
	if strings.Contains(out, "(truncated)") {
		t.Errorf("short message must not carry truncation marker: %q", out)
	}
}

// TestBuildTitleTranscript_TranscriptOnly pins the contract that the
// transcript-only builder excludes the instruction block — the
// instructions are meant to ride in the system prompt (claude path),
// not inside the user message. If they leak back in, the savings
// from --system-prompt evaporate.
func TestBuildTitleTranscript_TranscriptOnly(t *testing.T) {
	out, err := BuildTitleTranscript([]string{"first", "second"})
	if err != nil {
		t.Fatalf("BuildTitleTranscript: %v", err)
	}
	if !strings.Contains(out, "[Message 1] first") || !strings.Contains(out, "[Message 2] second") {
		t.Errorf("transcript missing numbered messages: %q", out)
	}
	if strings.Contains(out, "Summarize") || strings.Contains(out, "SKIP") {
		t.Errorf("transcript leaked instruction text: %q", out)
	}
}

// TestBuildTitleTranscript_NoPrompts mirrors the empty-input
// contract for the standalone transcript builder.
func TestBuildTitleTranscript_NoPrompts(t *testing.T) {
	if _, err := BuildTitleTranscript(nil); err == nil {
		t.Error("expected error for nil userPrompts, got nil")
	}
}

// TestTitleSystemPrompt_KeepsRules sanity-checks that the system
// prompt still carries the two rules other layers depend on (the
// abstain sentinel + the "no tools" directive). Without these the
// model can either invent a title for a bare "hi" or try to invoke
// a tool that --tools "" no longer exposes.
func TestTitleSystemPrompt_KeepsRules(t *testing.T) {
	if !strings.Contains(TitleSystemPrompt, TitleAbstainSentinel) {
		t.Error("system prompt missing abstain sentinel")
	}
	if !strings.Contains(TitleSystemPrompt, "Do not use any tools") {
		t.Error("system prompt missing no-tools directive")
	}
}
