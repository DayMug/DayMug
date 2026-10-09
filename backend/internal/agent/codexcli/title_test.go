package codexcli

import (
	"context"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// stubBackend is a no-network agent.Backend used by the title tests.
// Only RunOneshot is exercised; the other methods exist purely so
// stubBackend satisfies the interface — they'd panic if called but
// the test paths never hit them.
type stubBackend struct {
	gotPrompt string
	gotReq    agent.RunRequest
	out       string
	err       error
}

func (b *stubBackend) Name() string                     { return "stub" }
func (b *stubBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b *stubBackend) Run(context.Context, string, string, chan<- agent.StreamEvent) error {
	return nil
}
func (b *stubBackend) RunWithSession(context.Context, string, string, agent.RunRequest, chan<- agent.StreamEvent) error {
	return nil
}
func (b *stubBackend) RunOneshot(_ context.Context, prompt, _ string, req agent.RunRequest) (string, error) {
	b.gotPrompt = prompt
	b.gotReq = req
	return b.out, b.err
}
func (b *stubBackend) SessionExists(string, string, string) bool    { return false }
func (b *stubBackend) SessionLogPath(string, string, string) string { return "" }

// TestCodexTitleGenerator_NoPrompts mirrors the claude side: empty
// input must surface the BuildTitleGenPrompt error without spawning
// the codex CLI.
func TestCodexTitleGenerator_NoPrompts(t *testing.T) {
	g := NewTitleGeneratorWithBackend(NewBackend(), "")
	if _, err := g.GenerateTitle(t.Context(), nil, agent.TitleAccount{}); err == nil {
		t.Fatal("expected error for empty prompts, got nil")
	}
}

// TestCodexTitleGenerator_UsesLunaModel verifies the generator drives
// the configured cheap model and threads the user message into the
// prompt template. Without the stub backend this would shell out to
// the real codex CLI.
func TestCodexTitleGenerator_UsesLunaModel(t *testing.T) {
	stub := &stubBackend{out: `"Codex Title"`}
	g := &titleGenerator{backend: stub, model: titleGenModel}

	got, err := g.GenerateTitle(t.Context(), []string{"design a cache eviction policy"}, agent.TitleAccount{})
	if err != nil {
		t.Fatalf("GenerateTitle: %v", err)
	}
	if got != "Codex Title" {
		t.Errorf("title = %q, want Codex Title", got)
	}
	if stub.gotReq.Model != titleGenModel {
		t.Errorf("model = %q, want %q", stub.gotReq.Model, titleGenModel)
	}
	if want := "[Message 1] design a cache eviction policy"; !strings.Contains(stub.gotPrompt, want) {
		t.Errorf("prompt missing user message: %q", stub.gotPrompt)
	}
}

// TestCodexTitleGenerator_ThreadsAccountIntoRunRequest pins down the
// per-account routing: ConfigDir and Env on TitleAccount must reach
// the RunRequest the backend sees. Without this the title path would
// silently ignore per-user provider bindings and every conversation's
// title would run under whichever CODEX_HOME the daymug process
// inherited at startup — exactly the bug 81a2585 fixed for chat but
// that we'd have re-introduced if title fell out of sync.
func TestCodexTitleGenerator_ThreadsAccountIntoRunRequest(t *testing.T) {
	stub := &stubBackend{out: "Title From Bound Account"}
	g := &titleGenerator{backend: stub, model: titleGenModel}

	account := agent.TitleAccount{
		ConfigDir: "/tmp/codex-acct-A",
		Env:       map[string]string{"OPENAI_API_KEY": "sk-test"},
	}
	if _, err := g.GenerateTitle(t.Context(), []string{"hello"}, account); err != nil {
		t.Fatalf("GenerateTitle: %v", err)
	}
	if stub.gotReq.ConfigDir != account.ConfigDir {
		t.Errorf("ConfigDir = %q, want %q", stub.gotReq.ConfigDir, account.ConfigDir)
	}
	if got := stub.gotReq.AccountEnv["OPENAI_API_KEY"]; got != "sk-test" {
		t.Errorf("AccountEnv[OPENAI_API_KEY] = %q, want sk-test", got)
	}
}

// TestCodexTitleGenerator_NilBackend produces a friendly error
// instead of a nil-pointer panic when constructed by hand without a
// backend. NewTitleGenerator() always wires one up; this guards
// against a future test or DI path that forgets to.
func TestCodexTitleGenerator_NilBackend(t *testing.T) {
	g := &titleGenerator{backend: nil, model: titleGenModel}
	if _, err := g.GenerateTitle(t.Context(), []string{"x"}, agent.TitleAccount{}); err == nil {
		t.Error("expected error when backend is nil")
	}
}

func TestNewTitleGeneratorWithBackendKeepsLunaDefault(t *testing.T) {
	got, ok := NewTitleGeneratorWithBackend(NewBackend(), "").(*titleGenerator)
	if !ok {
		t.Fatalf("NewTitleGeneratorWithBackend returned %T, want *titleGenerator", got)
	}
	if got.model != "gpt-5.6-luna" {
		t.Fatalf("default title model = %q, want gpt-5.6-luna", got.model)
	}
}
