package claudeagentsdk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

func TestBackendIdentityAndCapabilities(t *testing.T) {
	b := NewBackend()
	if got := b.Name(); got != "claude-agent-sdk" {
		t.Fatalf("Name() = %q", got)
	}
	caps := b.Capabilities()
	if !caps.SupportsCompaction || !caps.SupportsThinkingStream || !caps.SupportsRateLimitEvents || !caps.ReportsCostUSD {
		t.Fatalf("Capabilities() = %+v", caps)
	}
}

func TestMaterializeBridgeSurvivesTempCleanup(t *testing.T) {
	path, err := materializeBridge()
	if err != nil {
		t.Fatalf("materializeBridge: %v", err)
	}
	if path == "" {
		t.Fatal("materializeBridge returned an empty path")
	}
	// /tmp cleaners delete this out from under a long-running server; the next
	// turn must write it again instead of failing forever on a cached path.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	again, err := materializeBridge()
	if err != nil {
		t.Fatalf("materializeBridge after cleanup: %v", err)
	}
	if again != path {
		t.Fatalf("bridge path changed: %q → %q", path, again)
	}
	if _, err := os.Stat(again); err != nil {
		t.Fatalf("bridge was not rewritten: %v", err)
	}
}

func TestResolveSDKModuleFindsAGlobalInstall(t *testing.T) {
	root := fakeSDKInstall(t, "9.9.9")
	withSearchDirs(t, []string{filepath.Dir(root)}, root)

	module, ok := resolveSDKModule()
	if !ok {
		t.Fatal("resolveSDKModule missed a global install")
	}
	if module.Root != root {
		t.Fatalf("Root = %q, want the node_modules dir %q", module.Root, root)
	}
	if module.Version != "9.9.9" {
		t.Fatalf("Version = %q", module.Version)
	}
	if !strings.HasSuffix(module.Entry, filepath.Join(sdkScope, sdkPackage, sdkEntry)) {
		t.Fatalf("Entry = %q", module.Entry)
	}
}

func TestResolveSDKModuleIgnoresAnAmbientOverrideInTests(t *testing.T) {
	t.Setenv(sdkModuleEnv, "/host/lib/node_modules/@anthropic-ai/claude-agent-sdk/sdk.mjs")
	root := fakeSDKInstall(t, "9.9.9")
	withSearchDirs(t, nil, root)

	module, ok := resolveSDKModule()
	if !ok {
		t.Fatal("resolveSDKModule missed the fake install")
	}
	if module.Root != root {
		t.Fatalf("Root = %q — the host environment leaked past the test seam", module.Root)
	}
}

func TestSDKModuleOverrideKeepsItsNodeModulesRoot(t *testing.T) {
	root := fakeSDKInstall(t, "1.2.3")
	entry := filepath.Join(root, sdkScope, sdkPackage, sdkEntry)
	withSearchDirs(t, nil, "")
	t.Setenv(sdkModuleEnv, entry)

	module, ok := resolveSDKModule()
	if !ok {
		t.Fatal("an explicit override must resolve")
	}
	if module.Root != root {
		t.Fatalf("Root = %q, want %q — the whole tree has to be bound, not just the package", module.Root, root)
	}
}

func TestJailedRunBindsTheSDKAndPinsItsPath(t *testing.T) {
	root := fakeSDKInstall(t, "1.0.0")
	withSearchDirs(t, []string{root}, root)

	sandbox := &captureSandbox{}
	spawner := &captureSpawner{}
	backend := NewBackend()
	ch := make(chan agent.StreamEvent, 8)
	if err := backend.RunWithSession(context.Background(), "hi", t.TempDir(), agent.RunRequest{
		Sandbox: sandbox, Spawner: spawner, JailRoot: t.TempDir(), ConfigDir: "/account",
	}, ch); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	for range ch {
	}

	var boundSDK, boundBridge bool
	for _, bind := range sandbox.opts.ExtraBinds {
		if bind.Source == root {
			boundSDK = true
		}
		if strings.HasSuffix(bind.Source, ".mjs") {
			boundBridge = true
		}
	}
	if !boundSDK {
		t.Fatalf("the SDK tree was not bound into the jail: %#v", sandbox.opts.ExtraBinds)
	}
	if !boundBridge {
		t.Fatalf("the bridge script was not bound into the jail: %#v", sandbox.opts.ExtraBinds)
	}
	if !hasEnv(spawner.req.Env, sdkModuleEnv) {
		t.Fatalf("%s was not passed to the child; it would have to shell out to npm inside the jail", sdkModuleEnv)
	}
}

func TestTitleTurnAsksForAMinimalSession(t *testing.T) {
	spawner := &captureSpawner{}
	gen := NewTitleGenerator("").(*titleGenerator)
	gen.runner = &runner{}

	// Drive the shared bridge path directly: the generator's own opts are what
	// this asserts, and a real SDK process is not available in tests.
	ch := make(chan agent.StreamEvent, 8)
	req := bridgeRequest{Prompt: "name this", Model: "claude-haiku-4-5", SystemPrompt: agent.TitleSystemPrompt, Minimal: true}
	if err := gen.runner.stream(context.Background(), req, "", agent.RunRequest{Spawner: spawner}, ch); err != nil {
		t.Fatalf("stream: %v", err)
	}
	close(ch)

	var sent bridgeRequest
	if err := json.Unmarshal([]byte(spawner.req.InitialStdin), &sent); err != nil {
		t.Fatalf("bridge request is not valid JSON: %v", err)
	}
	if !sent.Minimal {
		t.Fatal("title turns must request the minimal session, or every title loads settings, MCP and CLAUDE.md")
	}
	if sent.SystemPrompt != agent.TitleSystemPrompt {
		t.Fatalf("system prompt = %q", sent.SystemPrompt)
	}
}

func TestChatTurnKeepsTheFullSession(t *testing.T) {
	spawner := &captureSpawner{}
	backend := NewBackend()
	ch := make(chan agent.StreamEvent, 8)
	if err := backend.RunWithSession(context.Background(), "hi", "/workspace", agent.RunRequest{
		Spawner: spawner, SessionID: "abc", IsResume: true, Model: "claude-opus-5",
	}, ch); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	for range ch {
	}
	var sent bridgeRequest
	if err := json.Unmarshal([]byte(spawner.req.InitialStdin), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Minimal {
		t.Fatal("a chat turn must not be stripped to the minimal session")
	}
	if !sent.Resume || sent.SessionID != "abc" {
		t.Fatalf("resume fields lost: %#v", sent)
	}
	if sent.Effort != "" {
		t.Fatalf("unset effort was forced to %q", sent.Effort)
	}
}

func TestMCPConfigIsMaterializedOutsideClaudeChildArgv(t *testing.T) {
	root := fakeSDKInstallWithSource(t, "9.9.9", `
export function query({ prompt, options }) {
  return (async function* () {
    for await (const message of prompt) {
      const probe = [
        'const fs = require("node:fs");',
        'const index = process.argv.indexOf("--mcp-config");',
        'const path = process.argv[index + 1];',
        'const stat = fs.statSync(path);',
        'process.stdout.write(JSON.stringify({ path, content: fs.readFileSync(path, "utf8"), mode: stat.mode & 0o777 }));',
      ].join("");
      const child = options.spawnClaudeCodeProcess({
        command: process.execPath,
        args: ["-e", probe, "--", "--mcp-config", JSON.stringify({ mcpServers: options.mcpServers })],
        cwd: process.cwd(),
        env: process.env,
        signal: new AbortController().signal,
      });
      child.stdin.end();
      let output = "";
      for await (const chunk of child.stdout) output += chunk;
      yield {
        type: "result",
        subtype: "success",
        is_error: false,
        result: output,
        user_message_uuid: message.uuid,
      };
      return;
    }
  })();
}
`)
	withSearchDirs(t, []string{root}, root)

	const secret = "mcp-secret-must-not-be-in-argv"
	configPath := filepath.Join(t.TempDir(), "mcp.json")
	config := `{"mcpServers":{"private":{"type":"http","url":"https://mcp.invalid","headers":{"Authorization":"Bearer ` + secret + `"}}}}`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	backend := NewBackend()
	outputCh := make(chan agent.StreamEvent, 16)
	if err := backend.RunWithSession(context.Background(), "inspect argv", t.TempDir(), agent.RunRequest{
		McpConfigPath: configPath,
	}, outputCh); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}

	var payload struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Mode    uint32 `json:"mode"`
	}
	for event := range outputCh {
		if event.Kind == agent.KindResult {
			if err := json.Unmarshal([]byte(event.Content), &payload); err != nil {
				t.Fatalf("decode child probe: %v (%q)", err, event.Content)
			}
		}
	}
	if payload.Path == "" || strings.Contains(payload.Path, secret) {
		t.Fatalf("Claude child mcp argv = %q, want a non-secret file path", payload.Path)
	}
	if !strings.Contains(payload.Content, secret) {
		t.Fatalf("materialized MCP config lost its credential: %q", payload.Content)
	}
	if payload.Mode != 0o600 {
		t.Fatalf("materialized MCP config mode = %#o, want 0600", payload.Mode)
	}
	if _, err := os.Stat(payload.Path); !os.IsNotExist(err) {
		t.Fatalf("materialized MCP config was not removed after child exit: %v", err)
	}
}

func TestSteerTurnStreamsInputIntoActiveSDKQuery(t *testing.T) {
	root := fakeSDKInstallWithSource(t, "9.9.9", `
export async function* query({ prompt }) {
  let count = 0;
  for await (const message of prompt) {
    count += 1;
    if (count === 2) {
      yield {
        type: "result",
        subtype: "success",
        is_error: false,
        result: "steered:" + message.message.content,
      };
    }
  }
}
`)
	withSearchDirs(t, []string{root}, root)

	backend := NewBackend()
	r := backend.(*runner)
	steerer, ok := backend.(agent.TurnSteeringBackend)
	if !ok {
		t.Fatal("Claude Agent SDK backend does not expose streaming input")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	outputCh := make(chan agent.StreamEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		errCh <- backend.RunWithSession(ctx, "first", t.TempDir(), agent.RunRequest{
			ControlID: "conv-steer",
		}, outputCh)
	}()
	waitForActiveBridge(t, r, "conv-steer")

	if err := steerer.SteerTurn(ctx, "conv-steer", "message-2", "focus on tests"); err != nil {
		t.Fatalf("SteerTurn: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	var result string
	for event := range outputCh {
		if event.Kind == agent.KindResult {
			result = event.Content
		}
	}
	if result != "steered:focus on tests" {
		t.Fatalf("result = %q", result)
	}
	if err := steerer.SteerTurn(context.Background(), "conv-steer", "message-3", "late"); !errors.Is(err, agent.ErrNoActiveTurn) {
		t.Fatalf("late SteerTurn = %v, want ErrNoActiveTurn", err)
	}
}

func TestAnswerUserQuestionResumesSDKQuery(t *testing.T) {
	root := fakeSDKInstallWithSource(t, "9.9.9", `
export async function* query({ prompt, options }) {
  for await (const message of prompt) {
    if (!message) return;
    const permission = await options.canUseTool("AskUserQuestion", {
      questions: [{
        question: "Which environment?",
        header: "Environment",
        options: [{ label: "Staging", description: "Safer" }],
        multiSelect: false,
      }],
    }, { requestId: "sdk-question-1" });
    yield {
      type: "result",
      subtype: "success",
      is_error: false,
      result: permission.updatedInput.answers["Which environment?"],
    };
    return;
  }
}
`)
	withSearchDirs(t, []string{root}, root)

	backend := NewBackend()
	responder := backend.(agent.UserQuestionBackend)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	outputCh := make(chan agent.StreamEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		errCh <- backend.RunWithSession(ctx, "deploy", t.TempDir(), agent.RunRequest{
			ControlID: "conv-question", EnableUserQuestions: true,
		}, outputCh)
	}()

	var result string
	for event := range outputCh {
		switch event.Kind {
		case agent.KindUserQuestion:
			var question agent.UserQuestionRequest
			if err := json.Unmarshal([]byte(event.Content), &question); err != nil {
				t.Fatal(err)
			}
			if question.RequestID != "sdk-question-1" || question.Questions[0].ID != "q1" {
				t.Fatalf("question = %+v", question)
			}
			if err := responder.AnswerUserQuestion(ctx, "conv-question", question.RequestID, map[string][]string{"q1": {"Staging"}}); err != nil {
				t.Fatalf("AnswerUserQuestion: %v", err)
			}
		case agent.KindResult:
			result = event.Content
		}
	}
	if err := <-errCh; err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	if result != "Staging" {
		t.Fatalf("result = %q", result)
	}
}

func TestBridgeStillSupportsOneShotInput(t *testing.T) {
	root := fakeSDKInstallWithSource(t, "9.9.9", `
export async function* query({ prompt }) {
  for await (const message of prompt) {
    yield {
      type: "result",
      subtype: "success",
      is_error: false,
      result: "oneshot:" + message.message.content,
    };
  }
}
`)
	withSearchDirs(t, []string{root}, root)

	backend := NewBackend()
	outputCh := make(chan agent.StreamEvent, 16)
	if err := backend.RunWithSession(context.Background(), "hello", t.TempDir(), agent.RunRequest{}, outputCh); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	var result string
	for event := range outputCh {
		if event.Kind == agent.KindResult {
			result = event.Content
		}
	}
	if result != "oneshot:hello" {
		t.Fatalf("result = %q", result)
	}
}

func TestPtySpawnerIsDowngradedToPipes(t *testing.T) {
	if _, isPty := effectiveSpawner(agent.RunRequest{Spawner: &agent.PtySpawner{}}).(*agent.PtySpawner); isPty {
		t.Fatal("a pty would echo the bridge request back into the NDJSON stream and merge stderr away")
	}
	if _, ok := effectiveSpawner(agent.RunRequest{}).(agent.PipeSpawner); !ok {
		t.Fatal("a nil spawner must default to pipes")
	}
	custom := &captureSpawner{}
	if got := effectiveSpawner(agent.RunRequest{Spawner: custom}); got != custom {
		t.Fatal("a non-pty spawner (tests, future transports) must pass through")
	}
}

func fakeSDKInstall(t *testing.T, version string) string {
	return fakeSDKInstallWithSource(t, version, "export const query = () => {}\n")
}

func fakeSDKInstallWithSource(t *testing.T, version, source string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "node_modules")
	dir := filepath.Join(root, sdkScope, sdkPackage)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sdkEntry), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"name":"@anthropic-ai/claude-agent-sdk","version":"` + version + `"}`)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func waitForActiveBridge(t *testing.T, r *runner, controlID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r.activeMu.RLock()
		active := r.active[controlID]
		r.activeMu.RUnlock()
		if active != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Agent SDK bridge did not become active")
}

// withSearchDirs swaps the resolution seam and clears the memoised answer, in
// both directions, so tests don't leak a fake install into each other.
//
// It also drops any ambient sdkModuleEnv: that override short-circuits the seam
// entirely, so a developer box (or a DayMug server shell) that exports it would
// otherwise resolve the real global SDK and fail every test here.
func withSearchDirs(t *testing.T, dirs []string, primary string) {
	t.Helper()
	t.Setenv(sdkModuleEnv, "")
	if primary != "" {
		dirs = append([]string{primary}, dirs...)
	}
	previous := sdkSearchDirs
	sdkSearchDirs = func() []string { return dirs }
	sdkModuleMu.Lock()
	sdkModuleCached = nil
	sdkModuleMu.Unlock()
	t.Cleanup(func() {
		sdkSearchDirs = previous
		sdkModuleMu.Lock()
		sdkModuleCached = nil
		sdkModuleMu.Unlock()
	})
}

func hasEnv(env []string, key string) bool {
	for _, entry := range env {
		if strings.HasPrefix(entry, key+"=") && len(entry) > len(key)+1 {
			return true
		}
	}
	return false
}

type captureSandbox struct{ opts agent.WrapOpts }

func (s *captureSandbox) Wrap(argv []string, _ string, opts agent.WrapOpts) ([]string, []string, error) {
	s.opts = opts
	return argv, nil, nil
}

type captureSpawner struct{ req agent.SpawnRequest }

func (s *captureSpawner) Spawn(_ context.Context, req agent.SpawnRequest) (agent.RunningProcess, error) {
	s.req = req
	return &silentProcess{}, nil
}

type silentProcess struct{}

func (*silentProcess) Stdout() io.Reader        { return strings.NewReader("") }
func (*silentProcess) Stderr() io.Reader        { return strings.NewReader("") }
func (*silentProcess) Stdin() io.WriteCloser    { return nopWriteCloser{} }
func (*silentProcess) Wait() error              { return nil }
func (*silentProcess) Signal(_ os.Signal) error { return nil }

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

func TestBridgeDiagnosticKeepsOnlyTheFailure(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   string
	}{
		{
			name: "node warning is dropped",
			stderr: "(node:2071153) [CLAUDE_SDK_CAN_USE_TOOL_SHADOWED] Warning: canUseTool will not be invoked\n" +
				"(Use `node --trace-warnings ...` to show where the warning was created)\n" +
				"CAS bridge: You've hit your session limit\n",
			want: "CAS bridge: You've hit your session limit",
		},
		{
			name:   "an unrecognised crash is kept verbatim",
			stderr: "TypeError: x is not a function\n    at query (/tmp/sdk.mjs:3:9)\n",
			want:   "TypeError: x is not a function\nat query (/tmp/sdk.mjs:3:9)",
		},
		{
			name:   "warnings alone leave nothing to report",
			stderr: "(node:12) [DEP0040] Warning: punycode is deprecated\n",
			want:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bridgeDiagnostic(tc.stderr); got != tc.want {
				t.Fatalf("bridgeDiagnostic() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBridgeFailureReportsTheSDKErrorNotTheNodeWarning(t *testing.T) {
	root := fakeSDKInstallWithSource(t, "9.9.9", `
process.emitWarning("canUseTool will not be invoked: permissionMode 'bypassPermissions' auto-approves every tool call", {
  code: "CLAUDE_SDK_CAN_USE_TOOL_SHADOWED",
});
export async function* query() {
  throw new Error("Claude Code returned an error result: You've hit your session limit");
}
`)
	withSearchDirs(t, []string{root}, root)

	outputCh := make(chan agent.StreamEvent, 8)
	errCh := make(chan error, 1)
	go func() {
		errCh <- NewBackend().RunWithSession(context.Background(), "hi", t.TempDir(), agent.RunRequest{}, outputCh)
	}()
	for range outputCh {
	}
	err := <-errCh
	if err == nil {
		t.Fatal("a bridge that throws must surface an error")
	}
	if !strings.Contains(err.Error(), "You've hit your session limit") {
		t.Fatalf("error = %q, want the SDK's own message", err)
	}
	if strings.Contains(err.Error(), "CLAUDE_SDK_CAN_USE_TOOL_SHADOWED") || strings.Contains(err.Error(), "trace-warnings") {
		t.Fatalf("error = %q — node's process warning is noise, not a diagnosis", err)
	}
}

// A stalled turn is killed by DayMug itself, so the bridge always dies with a
// non-zero status and an abort on stderr. That exit is a consequence of the
// stall, and reporting it instead leaves the user with "exit status 1" for a
// failure that has a precise name.
func TestStallOutranksTheBridgeExitItCauses(t *testing.T) {
	root := fakeSDKInstallWithSource(t, "9.9.9", `
export async function* query({ options }) {
  // A real SDK holds the event loop open with its child process; without a
  // handle of its own this stub would reach EOF on stdin and exit cleanly,
  // which is not the failure under test.
  const alive = setInterval(() => {}, 1000);
  await new Promise((_, reject) => {
    options.abortController.signal.addEventListener("abort", () => {
      clearInterval(alive);
      reject(new Error("Operation aborted"));
    }, { once: true });
  });
}
`)
	withSearchDirs(t, []string{root}, root)

	outputCh := make(chan agent.StreamEvent, 8)
	errCh := make(chan error, 1)
	go func() {
		errCh <- NewBackend().RunWithSession(context.Background(), "hi", t.TempDir(), agent.RunRequest{
			StallTimeout: 50 * time.Millisecond, MaxSilentTimeout: 60 * time.Millisecond,
		}, outputCh)
	}()
	for range outputCh {
	}
	err := <-errCh
	if err == nil {
		t.Fatal("a silent bridge must fail the turn")
	}
	if !strings.Contains(err.Error(), "upstream unreachable") {
		t.Fatalf("error = %q, want the stall diagnosis", err)
	}
	if strings.Contains(err.Error(), "exit status") || strings.Contains(err.Error(), "Operation aborted") {
		t.Fatalf("error = %q — that is DayMug's own SIGTERM echoing back", err)
	}
}

// A shell command that reads stdin must see EOF rather than inherit the CLI's
// never-closing stream-json channel — the fake runs the rewritten command with
// a stdin pipe it never ends, which is that channel's shape.
func TestShellToolCommandsGetDevNullStdin(t *testing.T) {
	root := fakeSDKInstallWithSource(t, "9.9.9", `
import { spawn } from "node:child_process";
export async function* query({ prompt, options }) {
  for await (const message of prompt) {
    const [entry] = options.hooks.PreToolUse;
    const matcher = new RegExp("^(?:" + entry.matcher + ")$");
    const run = async (toolName, command) => {
      if (!matcher.test(toolName)) return "unmatched";
      const out = await entry.hooks[0]({ hook_event_name: "PreToolUse", tool_name: toolName, tool_input: { command } });
      const rewritten = out.hookSpecificOutput.updatedInput.command;
      const again = await entry.hooks[0]({ tool_input: { command: rewritten } });
      if (again.hookSpecificOutput) return "guard applied twice";
      const child = spawn("bash", ["-c", rewritten], { stdio: ["pipe", "pipe", "inherit"] });
      let stdout = "";
      for await (const chunk of child.stdout) stdout += chunk;
      return stdout;
    };
    const bash = await run("Bash", 'cat; echo "a <= 0"');
    const monitor = await run("Monitor", "cat; echo watched");
    const read = await run("Read", "cat");
    yield {
      type: "result",
      subtype: "success",
      is_error: false,
      result: JSON.stringify({ bash, monitor, read }),
      user_message_uuid: message.uuid,
    };
    return;
  }
}
`)
	withSearchDirs(t, []string{root}, root)

	backend := NewBackend()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	outputCh := make(chan agent.StreamEvent, 16)
	if err := backend.RunWithSession(ctx, "run shell", t.TempDir(), agent.RunRequest{}, outputCh); err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	var got struct{ Bash, Monitor, Read string }
	for event := range outputCh {
		if event.Kind == agent.KindResult {
			if err := json.Unmarshal([]byte(event.Content), &got); err != nil {
				t.Fatalf("decode result: %v (%q)", err, event.Content)
			}
		}
	}
	if got.Bash != "a <= 0\n" || got.Monitor != "watched\n" {
		t.Fatalf("shell outputs = %+v, want stdin at EOF for Bash and Monitor", got)
	}
	if got.Read != "unmatched" {
		t.Fatalf("Read tool matched the shell stdin guard: %q", got.Read)
	}
}
