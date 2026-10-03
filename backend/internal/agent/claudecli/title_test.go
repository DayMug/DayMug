package claudecli

import (
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// TestClaudeTitleGenerator_NoPrompts ensures the generator surfaces
// the empty-input error from BuildTitleGenPrompt rather than shelling
// out to claude with a bare template. Cheap fast test — does not
// touch the network or fork the CLI.
func TestClaudeTitleGenerator_NoPrompts(t *testing.T) {
	g := NewTitleGeneratorWithModel("")
	if _, err := g.GenerateTitle(t.Context(), nil, agent.TitleAccount{}); err == nil {
		t.Fatal("expected error for empty prompts, got nil")
	}
}
