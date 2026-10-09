package codexcli

import (
	"context"
	"fmt"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

const (
	titleGenTimeout = 30 * time.Second
	titleGenModel   = "gpt-5.6-luna"
)

type titleGenerator struct {
	backend agent.Backend
	model   string
}

// NewTitleGeneratorWithBackend lets the server share its selected Codex
// transport with title generation. In app-server mode this is what keeps title
// turns on the same account-scoped process pool as normal chat turns.
func NewTitleGeneratorWithBackend(backend agent.Backend, model string) agent.TitleGenerator {
	if model == "" {
		model = titleGenModel
	}
	return &titleGenerator{backend: backend, model: model}
}

func (g *titleGenerator) GenerateTitle(ctx context.Context, userPrompts []string, account agent.TitleAccount) (string, error) {
	prompt, err := agent.BuildTitleGenPrompt(userPrompts)
	if err != nil {
		return "", err
	}
	if g.backend == nil {
		return "", fmt.Errorf("title gen codex: no backend")
	}

	cctx, cancel := context.WithTimeout(ctx, titleGenTimeout)
	defer cancel()

	// The codex backend maps ConfigDir → CODEX_HOME and merges
	// AccountEnv on top of the child env (streamcommon.RunEnv), so
	// threading the account fields through RunRequest is enough — no
	// special-casing needed here.
	out, err := g.backend.RunOneshot(cctx, prompt, "", agent.RunRequest{
		Model:      g.model,
		ConfigDir:  account.ConfigDir,
		AccountEnv: account.Env,
	})
	if err != nil {
		return "", fmt.Errorf("title gen codex: %w", err)
	}
	return agent.CleanTitle(out), nil
}
