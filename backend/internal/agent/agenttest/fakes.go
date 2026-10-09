// Package agenttest provides shared agent.Backend / title-generator fakes and
// the canonical test provider config used by tests across packages.
package agenttest

import (
	"context"
	"sync"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
)

const (
	Model = "claude-test-model"
	// Account is the single account Config() declares.
	Account = "acc1"
)

// CallRecord captures what a ScriptedBackend was invoked with, across retries.
type CallRecord struct {
	N        int
	LastOpts agent.RunRequest
	LastWD   string
	Prompts  []string
}

// ScriptedBackend emits a fixed (or per-call) result then closes the channel,
// standing in for a real CLI run. When rec is set it records each invocation so
// tests can assert chat-mode flags and the cwd, and serve different outputs per
// retry via results.
type ScriptedBackend struct {
	Result  string
	Results []string
	Rec     *CallRecord
}

func (ScriptedBackend) Name() string                     { return "scripted" }
func (ScriptedBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b ScriptedBackend) RunWithSession(_ context.Context, prompt, wd string, opts agent.RunRequest, ch chan<- agent.StreamEvent) error {
	out := b.Result
	idx := 0
	if b.Rec != nil {
		idx = b.Rec.N
		b.Rec.N++
		b.Rec.LastOpts = opts
		b.Rec.LastWD = wd
		b.Rec.Prompts = append(b.Rec.Prompts, prompt)
	}
	if idx < len(b.Results) {
		out = b.Results[idx]
	} else if len(b.Results) > 0 {
		out = b.Results[len(b.Results)-1]
	}
	ch <- agent.StreamEvent{Kind: agent.KindResult, Content: out}
	close(ch)
	return nil
}
func (b ScriptedBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return b.Result, nil
}
func (ScriptedBackend) SessionExists(string, string, string) bool    { return false }
func (ScriptedBackend) SessionLogPath(string, string, string) string { return "" }

// Config returns the one-account fake deployment used across handler and
// bridge tests. Model lists no longer live on config.Provider, so a test that
// needs acc1 to actually serve Model must also install the registry — see
// service.SetModelOverrides. Account names it declares are exported as
// constants so those registries can be keyed without restating literals.
func Config() *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			{Name: Account, Type: config.CLITypeClaude, MaxConcurrent: 2},
		},
	}
}

type FakeTitler struct {
	mu         sync.Mutex
	Calls      int
	GotInput   [][]string
	GotAccount []agent.TitleAccount
	Out        string
	Err        error
}

func (f *FakeTitler) GenerateTitle(_ context.Context, prompts []string, account agent.TitleAccount) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls++
	cp := append([]string(nil), prompts...)
	f.GotInput = append(f.GotInput, cp)
	f.GotAccount = append(f.GotAccount, account)
	return f.Out, f.Err
}

func (f *FakeTitler) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Calls
}
