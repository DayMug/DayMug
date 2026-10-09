package claudecli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
)

const titleGenTimeout = 30 * time.Second

// DefaultTitleModel is the cheap model conversation titles are generated with.
// Exported so every Claude transport (CLI, Agent SDK) names conversations with
// the same model when the deployment has no summary_model override.
const DefaultTitleModel = "claude-haiku-4-5"

type titleGenerator struct {
	binary string
	model  string
}

// NewTitleGeneratorWithModel is NewTitleGenerator with a deployment-specific
// small model override. Empty keeps the Claude default.
func NewTitleGeneratorWithModel(model string) agent.TitleGenerator {
	if model == "" {
		model = DefaultTitleModel
	}
	return &titleGenerator{binary: "claude", model: model}
}

// TitleGenerator implements agent.TitleBackend: titles run the same CLI with
// the minimal flag set below instead of a full RunOneshot session.
func (r *runner) TitleGenerator(model string) agent.TitleGenerator {
	return NewTitleGeneratorWithModel(model)
}

func (g *titleGenerator) GenerateTitle(ctx context.Context, userPrompts []string, account agent.TitleAccount) (string, error) {
	// Use the combined instruction+transcript prompt as the user
	// message. The --system-prompt alone was not enough to stop the
	// model from treating the transcript as a real chat turn and
	// answering it — observed in production where a "请写一个斐波那契
	// 函数" first message caused the cheap title model to emit the
	// title and then a full Python tutorial, burning output tokens.
	// Repeating the "summarize this, output only the title" instruction
	// in the user message reliably collapses the response to one line.
	prompt, err := agent.BuildTitleGenPrompt(userPrompts)
	if err != nil {
		return "", err
	}

	cctx, cancel := context.WithTimeout(ctx, titleGenTimeout)
	defer cancel()

	// agent.ResolveAgentBinary mirrors what the main runner does, so a
	// systemd unit that doesn't have ~/.nvm on PATH still finds the
	// claude binary. Without this the title generator broke under
	// exactly the deployments that the main runner already fixed.
	//
	// Per-flag rationale, with measured contribution from a /tmp probe
	// (claude-haiku-4-5, "hello" prompt, this repo's machine — your
	// numbers will vary with how many MCP servers / tools you have
	// installed, but the *shape* holds):
	//
	//   baseline (no flags):                                  ~68.8k tokens
	//   + --strict-mcp-config --mcp-config '{...empty...}':   −38k (biggest)
	//     Forces claude to ignore every MCP server in user /
	//     project / plugin settings. MCP tool schemas from search,
	//     storage, and productivity servers dominate the input — even one
	//     popular server adds thousands of tokens.
	//   + --tools "":                                         −24k
	//     Disables every built-in tool registration (Read, Edit,
	//     Bash, Glob, Grep, WebSearch, …). Title gen doesn't call
	//     tools, so all that schema is dead weight.
	//   + --system-prompt <minimal>:                          −5.8k
	//     Replaces Claude Code's default system prompt — drops the
	//     agent framing, env info, CLAUDE.md contents, and
	//     dynamic per-machine sections we don't need.
	//   + --dangerously-skip-permissions:                     (~0)
	//     Pre-existing; non-interactive safety belt for -p under
	//     our sandbox-disabled deployment.
	//
	//   net: ~68.8k → ~0.8k (~99% reduction).
	//
	// Notably we do NOT pass:
	//   --bare: forces auth onto ANTHROPIC_API_KEY/apiKeyHelper
	//     and skips keychain/OAuth, which would break the per-user
	//     CLAUDE_CONFIG_DIR bindings the generator runs under.
	//   --disable-slash-commands: measured zero impact once --tools
	//     "" is set (slash commands are not in the system prompt
	//     under -p mode).
	cmd := exec.CommandContext(cctx, agent.ResolveAgentBinary(g.binary),
		"-p",
		"--model", g.model,
		"--system-prompt", agent.TitleSystemPrompt,
		"--tools", "",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers": {}}`,
		"--dangerously-skip-permissions",
	)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = titleEnv(account)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// claude writes "weekly limit" and other auth/quota errors to
		// stdout, not stderr, so an empty-stderr exit=1 used to
		// surface as a bare "exit status 1" in the journal — the
		// actual cause was invisible. Surface whichever side has
		// output so the next rate-limit incident is diagnosable from
		// the logs alone.
		if stderr.Len() > 0 {
			return "", fmt.Errorf("title gen claude: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		if stdout.Len() > 0 {
			return "", fmt.Errorf("title gen claude: %w: %s", err, strings.TrimSpace(stdout.String()))
		}
		return "", fmt.Errorf("title gen claude: %w", err)
	}

	return agent.CleanTitle(stdout.String()), nil
}

// titleEnv assembles the child env, layering account overrides on top
// of the daymug process env. Mirrors streamcommon.RunEnv: a
// non-empty ConfigDir wins over any inherited CLAUDE_CONFIG_DIR, then
// YAML-declared extras (account.Env) win over both — operators
// occasionally need to inject API-key vars per account that beat the
// inherited shell value.
func titleEnv(account agent.TitleAccount) []string {
	env := os.Environ()
	if account.ConfigDir != "" {
		env = streamcommon.SetEnv(env, "CLAUDE_CONFIG_DIR", account.ConfigDir)
	}
	for k, v := range account.Env {
		env = streamcommon.SetEnv(env, k, v)
	}
	return env
}
