package claudeagentsdk

import (
	"context"
	"fmt"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/claudecli"
	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
)

const titleGenTimeout = 30 * time.Second

// titleGenerator names conversations through the same bridge chat turns use.
//
// The CLI generator shells out to the `claude` binary directly, which is a
// silent trap for the deployments most likely to pick agent-sdk in the first
// place: those where that binary is absent or unauthenticated. Titles would
// just never appear, with the reason buried in the server log.
type titleGenerator struct {
	runner *runner
	model  string
}

// NewTitleGenerator returns a generator bound to the Agent SDK transport.
// Empty model keeps the shared cheap-model default.
func NewTitleGenerator(model string) agent.TitleGenerator {
	if model == "" {
		model = claudecli.DefaultTitleModel
	}
	return &titleGenerator{runner: &runner{}, model: model}
}

// TitleGenerator implements agent.TitleBackend: titles go through the same
// bridge chat turns use, in its minimal mode rather than a full RunOneshot.
func (r *runner) TitleGenerator(model string) agent.TitleGenerator {
	return NewTitleGenerator(model)
}

func (g *titleGenerator) GenerateTitle(ctx context.Context, userPrompts []string, account agent.TitleAccount) (string, error) {
	prompt, err := agent.BuildTitleGenPrompt(userPrompts)
	if err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, titleGenTimeout)
	defer cancel()

	// Minimal mirrors the CLI generator's flag set (--tools "" /
	// --strict-mcp-config / --system-prompt), which measured a ~99% token
	// reduction against a default session. Without it every title would load
	// settings, MCP schemas and CLAUDE.md to write eight words.
	req := bridgeRequest{Prompt: prompt, Model: g.model, SystemPrompt: agent.TitleSystemPrompt, Minimal: true}
	opts := agent.RunRequest{Model: g.model, ConfigDir: account.ConfigDir, AccountEnv: account.Env}

	ch := make(chan agent.StreamEvent, 32)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		errCh <- g.runner.stream(cctx, req, "", opts, ch)
	}()
	out, err := streamcommon.DrainOneshot(ch, errCh)
	if err != nil {
		return "", fmt.Errorf("title gen CAS: %w", err)
	}
	return agent.CleanTitle(out), nil
}
