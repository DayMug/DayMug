// Package claudeagentsdk adapts Anthropic's TypeScript Claude Agent SDK to
// DayMug's provider-neutral Backend stream.
package claudeagentsdk

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/claudecli"
	"github.com/DayMug/DayMug/backend/internal/agent/sessionlog"
	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
)

var nodeBinary = "node"

//go:embed bridge.mjs
var bridgeSource []byte

type runner struct {
	activeMu sync.RWMutex
	active   map[string]*activeBridge
	// pool holds bridges parked between turns. Nil on the internal runners
	// (title generation) that construct a runner directly — a side run has
	// no conversation to park against and must never keep a process alive.
	pool *bridgePool
}

func NewBackend() agent.Backend {
	r := &runner{active: make(map[string]*activeBridge), pool: newBridgePool()}
	// Parked bridges belong to no turn, so the job drain that precedes a
	// clean shutdown finishes without touching them. Without this, a restart
	// or self-upgrade leaves one orphan per parked conversation, each still
	// holding the SDK session file the next boot is about to resume from.
	agent.RegisterCloser(r.pool.closeAll)
	// And the same blind spot on the way in: a graceful upgrade decides to
	// restart because no job is in flight, which a parked bridge never is.
	agent.RegisterResidentProbe(r.pool.activeResidentCount)
	return r
}
func (*runner) Name() string { return "claude-agent-sdk" }
func (*runner) Capabilities() agent.Capabilities {
	return agent.Capabilities{SupportsCompaction: true, SupportsThinkingStream: true, SupportsRateLimitEvents: true, ReportsContextUsage: true, ReportsCostUSD: true}
}

// The SDK runs the same Claude Code client as the CLI transport, so it writes
// the same projects/*.jsonl session log.
func (r *runner) SessionExists(workDir, sessionID, configDir string) bool {
	return sessionlog.Claude{}.SessionExists(workDir, sessionID, configDir)
}
func (r *runner) SessionLogPath(workDir, sessionID, configDir string) string {
	return sessionlog.ClaudePath(workDir, sessionID, configDir)
}

func (r *runner) RunOneshot(ctx context.Context, prompt, workDir string, opts agent.RunRequest) (string, error) {
	ch := make(chan agent.StreamEvent, 32)
	errCh := make(chan error, 1)
	go func() { errCh <- r.RunWithSession(ctx, prompt, workDir, opts, ch) }()
	return streamcommon.DrainOneshot(ch, errCh)
}

type bridgeRequest struct {
	Prompt              string         `json:"prompt"`
	MessageID           string         `json:"messageId,omitempty"`
	CWD                 string         `json:"cwd,omitempty"`
	SessionID           string         `json:"sessionId,omitempty"`
	Resume              bool           `json:"resume,omitempty"`
	Model               string         `json:"model,omitempty"`
	SystemPrompt        string         `json:"systemPrompt,omitempty"`
	ReadOnly            bool           `json:"readOnly,omitempty"`
	Effort              string         `json:"effort,omitempty"`
	MCPServers          map[string]any `json:"mcpServers,omitempty"`
	EnableUserQuestions bool           `json:"enableUserQuestions,omitempty"`
	// Minimal strips the session down to a single cheap completion: no
	// settings, no tools, no MCP, no session file, and SystemPrompt replaces
	// the Claude Code preset instead of appending to it. Used by title
	// generation, where loading a full agent context costs orders of magnitude
	// more tokens than the answer.
	Minimal bool `json:"minimal,omitempty"`
	// Resident tells the bridge not to close its input stream when a turn
	// ends, so the SDK query stays open and can start a later turn on its
	// own when a background task the turn armed finally settles. DayMug then
	// owns the process's lifetime instead of the turn owning it. Off for
	// every side run (oneshot, title, compact), which must exit at `result`.
	Resident bool `json:"resident,omitempty"`
}

const bridgeControlType = "daymug_control"

type bridgeControlFrame struct {
	Type      string               `json:"type"`
	Action    string               `json:"action,omitempty"`
	MessageID string               `json:"messageId,omitempty"`
	RequestID string               `json:"requestId,omitempty"`
	Content   string               `json:"content,omitempty"`
	Questions []agent.UserQuestion `json:"questions,omitempty"`
	Answers   map[string][]string  `json:"answers,omitempty"`
	Accepted  bool                 `json:"accepted,omitempty"`
	Error     string               `json:"error,omitempty"`
}

type activeBridge struct {
	mu        sync.Mutex
	stdin     io.WriteCloser
	pending   map[string]chan error
	questions map[string]struct{}
	// beforeSteer atomically extends the attached foreground turn before the
	// input reaches the SDK. Without that handoff, the previous result can
	// detach and tear down the bridge while the SDK is already dequeuing the
	// newly accepted message.
	beforeSteer func(string) error
	closed      bool
}

func newActiveBridge(stdin io.WriteCloser) *activeBridge {
	return &activeBridge{stdin: stdin, pending: make(map[string]chan error), questions: make(map[string]struct{})}
}

func (a *activeBridge) steer(ctx context.Context, messageID, input string) error {
	return a.sendAndWaitPrepared(ctx, "input:"+messageID, bridgeControlFrame{
		Type: bridgeControlType, Action: "input", MessageID: messageID, Content: input,
	}, func() error {
		if a.beforeSteer == nil {
			return nil
		}
		return a.beforeSteer(messageID)
	})
}

func (a *activeBridge) answer(ctx context.Context, requestID string, answers map[string][]string) error {
	a.mu.Lock()
	if _, ok := a.questions[requestID]; !ok || a.closed {
		a.mu.Unlock()
		return agent.ErrNoActiveQuestion
	}
	delete(a.questions, requestID)
	a.mu.Unlock()
	return a.sendAndWait(ctx, "answer:"+requestID, bridgeControlFrame{
		Type: bridgeControlType, Action: "answer", RequestID: requestID, Answers: answers,
	})
}

// interrupt aborts the turn the bridge is running without ending its query,
// so a resident process survives a Stop. The bridge cancels queued wakeups
// along with the running turn — a user pressing Stop means "stop everything",
// not "stop talking but keep waking up".
func (a *activeBridge) interrupt(ctx context.Context, messageID string) error {
	return a.sendAndWait(ctx, "interrupt:"+messageID, bridgeControlFrame{
		Type: bridgeControlType, Action: "interrupt", MessageID: messageID,
	})
}

func (a *activeBridge) addQuestion(requestID string) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || requestID == "" {
		return false
	}
	a.questions[requestID] = struct{}{}
	return true
}

func (a *activeBridge) sendAndWait(ctx context.Context, key string, control bridgeControlFrame) error {
	return a.sendAndWaitPrepared(ctx, key, control, nil)
}

func (a *activeBridge) sendAndWaitPrepared(
	ctx context.Context,
	key string,
	control bridgeControlFrame,
	prepare func() error,
) error {
	ack := make(chan error, 1)
	frame, err := json.Marshal(control)
	if err != nil {
		return fmt.Errorf("encode CAS control: %w", err)
	}

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return agent.ErrNoActiveTurn
	}
	if prepare != nil {
		if err := prepare(); err != nil {
			a.mu.Unlock()
			return err
		}
	}
	a.pending[key] = ack
	_, err = a.stdin.Write(append(frame, '\n'))
	if err != nil {
		delete(a.pending, key)
	}
	a.mu.Unlock()
	if err != nil {
		return fmt.Errorf("write CAS control: %w", err)
	}

	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		a.mu.Lock()
		if a.pending[key] == ack {
			delete(a.pending, key)
		}
		a.mu.Unlock()
		return ctx.Err()
	}
}

func (a *activeBridge) acknowledge(frame bridgeControlFrame) {
	if a == nil {
		return
	}
	// The key namespace has to match sendAndWait's exactly, so derive it from
	// the action rather than defaulting: an unrecognised ack that fell through
	// to the input namespace would resolve an unrelated pending steer.
	var key string
	switch frame.Action {
	case "input_ack":
		key = "input:" + frame.MessageID
	case "answer_ack":
		key = "answer:" + frame.RequestID
	case "interrupt_ack":
		key = "interrupt:" + frame.MessageID
	default:
		return
	}
	a.mu.Lock()
	ack := a.pending[key]
	delete(a.pending, key)
	a.mu.Unlock()
	if ack == nil {
		return
	}
	if frame.Accepted {
		ack <- nil
		return
	}
	if frame.Error == "" {
		frame.Error = "interactive request is no longer active"
	}
	ack <- fmt.Errorf("CAS rejected control: %s", frame.Error)
}

func (a *activeBridge) close() {
	// A run without a ControlID has no writable stdin and so no bridge to
	// close; teardown still walks this path.
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	_ = a.stdin.Close()
	pending := a.pending
	a.pending = make(map[string]chan error)
	a.questions = make(map[string]struct{})
	a.mu.Unlock()
	for _, ack := range pending {
		ack <- agent.ErrNoActiveTurn
	}
}

func (r *runner) RunWithSession(ctx context.Context, prompt, workDir string, opts agent.RunRequest, outputCh chan<- agent.StreamEvent) error {
	defer close(outputCh)
	req := bridgeRequest{Prompt: prompt, MessageID: uuid.NewString(), CWD: workDir, SessionID: opts.SessionID, Resume: opts.IsResume, Model: opts.Model, SystemPrompt: opts.SystemPrompt, ReadOnly: opts.ReadOnly, Effort: opts.ThinkLevel, EnableUserQuestions: opts.EnableUserQuestions}
	if opts.McpConfigPath != "" {
		data, readErr := os.ReadFile(opts.McpConfigPath)
		if readErr != nil {
			return fmt.Errorf("read MCP config: %w", readErr)
		}
		var cfg struct {
			MCPServers map[string]any `json:"mcpServers"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("parse MCP config: %w", err)
		}
		req.MCPServers = cfg.MCPServers
	}
	return r.stream(ctx, req, workDir, opts, outputCh)
}

// frameProcessor demultiplexes the bridge's stdout: DayMug's own control
// frames (question / *_ack) are handled here, everything else is provider
// stream-json for the shared claude parser.
func (r *runner) frameProcessor(b *residentBridge, processor *claudecli.StreamProcessor) func([]byte) []agent.StreamEvent {
	return func(line []byte) []agent.StreamEvent {
		var frame bridgeControlFrame
		if json.Unmarshal(line, &frame) == nil && frame.Type == bridgeControlType {
			if frame.Action == "question" {
				if b.active == nil || !b.active.addQuestion(frame.RequestID) {
					return nil
				}
				payload, err := json.Marshal(agent.UserQuestionRequest{RequestID: frame.RequestID, Questions: frame.Questions})
				if err != nil {
					return nil
				}
				return []agent.StreamEvent{{Kind: agent.KindUserQuestion, Content: string(payload)}}
			}
			b.active.acknowledge(frame)
			return nil
		}
		return processor.Process(line)
	}
}

// stream runs one turn against a bridge process, reusing a parked one when the
// conversation has one and spawning otherwise. It does not close outputCh —
// the caller owns that — but it does guarantee no further frame can be written
// to outputCh once it returns, which is what makes that close safe.
func (r *runner) stream(ctx context.Context, req bridgeRequest, workDir string, opts agent.RunRequest, outputCh chan<- agent.StreamEvent) error {
	// Residency needs a writable stdin (to reuse and to interrupt) and a
	// conversation to key on. Side runs have neither, and must keep exiting
	// at `result` the way they always have.
	res := opts.Residency
	if r.pool == nil || maxParkedBridges == 0 || opts.ControlID == "" {
		res = nil
	}
	req.Resident = res != nil
	fingerprint := bridgeFingerprint(opts, req)

	b, notice := r.pool.take(opts.ControlID, fingerprint)
	if notice != "" && res != nil && res.Notice != nil {
		res.Notice("Background work in this conversation was stopped because the " + notice + ".")
	}
	reused := b != nil
	if !reused {
		var err error
		if b, err = r.newResidentBridge(ctx, req, workDir, opts, fingerprint); err != nil {
			return err
		}
	}

	if res != nil {
		b.setWake(res.Wake)
		b.setNotice(res.Notice)
		b.setActivity(res.Activity)
	}

	turnEnd, err := b.attach(outputCh, req.MessageID)
	if err != nil {
		// The parked bridge died between the lookup and the attach. Retire
		// it and spawn, rather than failing a turn for a stale process.
		_ = b.markDead(err)
		if b, err = r.newResidentBridge(ctx, req, workDir, opts, fingerprint); err != nil {
			return err
		}
		reused = false
		if res != nil {
			b.setWake(res.Wake)
			b.setNotice(res.Notice)
			b.setActivity(res.Activity)
		}
		if turnEnd, err = b.attach(outputCh, req.MessageID); err != nil {
			_ = b.markDead(err)
			return err
		}
	}
	if reused {
		// The process is already running the conversation's SDK session, so
		// the prompt goes in as streaming input. Respawning with --resume
		// would kill the process group — and with it the very background
		// work that kept the bridge alive.
		if err := b.active.steer(ctx, req.MessageID, req.Prompt); err != nil {
			b.detach(err)
			_ = b.markDead(err)
			return fmt.Errorf("resume resident CAS session: %w", err)
		}
	}

	turnErr := b.awaitTurn(ctx, turnEnd)
	if r.parkOrRetire(b, res) {
		return turnErr
	}
	return b.finishTurn(turnErr)
}

// parkOrRetire keeps the process only when this turn left background work
// running AND the caller will pay for it. Everything else tears down, which is
// the pre-residency behaviour.
func (r *runner) parkOrRetire(b *residentBridge, res *agent.Residency) bool {
	if res == nil || b.liveTasks() == 0 || b.isDead() {
		return false
	}
	// A reused bridge still owns the slot acquired when this process first
	// parked. Reacquiring here would leak one slot per turn (setRelease would
	// overwrite the old release) and can reject a process that is already
	// legitimately resident when the account is at capacity.
	if b.hasRelease() {
		if r.pool.put(b) {
			if res.Parked != nil {
				res.Parked()
			}
			log.Printf("[claude-CAS] reparking conversation %s with %d live background task(s)", b.key, b.liveTasks())
			return true
		}
		if res.Notice != nil {
			res.Notice("This turn left background work running, but the resident session pool is shutting down. The background work was stopped.")
		}
		return false
	}
	if res.Permit != nil {
		if release, ok := res.Permit(); ok {
			b.setRelease(release)
			if r.pool.put(b) {
				if res.Parked != nil {
					res.Parked()
				}
				log.Printf("[claude-CAS] parking conversation %s with %d live background task(s)", b.key, b.liveTasks())
				return true
			}
			b.setRelease(nil)
			release()
		}
	}
	// Refusing to park means this turn's background work dies with the
	// process — the exact failure residency exists to prevent — so say so
	// rather than letting a long job vanish silently.
	if res.Notice != nil {
		res.Notice(fmt.Sprintf(
			"This turn left background work running, but the server is at its limit of %d resident sessions. The background work was stopped.",
			maxParkedBridges))
	}
	return false
}

// nodeProcessWarning matches the header line node prints for a process warning.
// The SDK emits one on every run that installs a hook, and concatenated in
// front of the bridge's own diagnostic it pushes the single line that names the
// failure — a session limit, a crash — past where anyone reads.
var nodeProcessWarning = regexp.MustCompile(`^\(node:\d+\) `)

// bridgeDiagnostic keeps the parts of the bridge's stderr that describe why it
// died. Unknown output is kept verbatim: a node stack trace is exactly what an
// operator needs when the bridge fails in a way DayMug has no name for.
func bridgeDiagnostic(stderr string) string {
	var kept []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case nodeProcessWarning.MatchString(line):
		case strings.HasPrefix(line, "(Use `node --trace-warnings"):
		default:
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func (r *runner) registerActiveBridge(controlID string, active *activeBridge) {
	if controlID == "" || active == nil {
		return
	}
	r.activeMu.Lock()
	if r.active == nil {
		r.active = make(map[string]*activeBridge)
	}
	r.active[controlID] = active
	r.activeMu.Unlock()
}

func (r *runner) unregisterActiveBridge(controlID string, active *activeBridge) {
	if active == nil {
		return
	}
	r.activeMu.Lock()
	if r.active[controlID] == active {
		delete(r.active, controlID)
	}
	r.activeMu.Unlock()
	active.close()
}

// SteerTurn sends a user message into the Agent SDK's streaming-input session.
// Unlike Codex turn/steer, Claude queues dynamic input on the live session; the
// bridge ACK confirms acceptance before DayMug removes the pending marker.
func (r *runner) SteerTurn(ctx context.Context, controlID, messageID, input string) error {
	r.activeMu.RLock()
	active := r.active[controlID]
	r.activeMu.RUnlock()
	if active == nil {
		return agent.ErrNoActiveTurn
	}
	return active.steer(ctx, messageID, input)
}

func (r *runner) AnswerUserQuestion(ctx context.Context, controlID, requestID string, answers map[string][]string) error {
	r.activeMu.RLock()
	active := r.active[controlID]
	r.activeMu.RUnlock()
	if active == nil {
		return agent.ErrNoActiveQuestion
	}
	return active.answer(ctx, requestID, answers)
}

func (r *runner) spawnBridge(ctx context.Context, req bridgeRequest, workDir string, opts agent.RunRequest) (agent.RunningProcess, context.CancelFunc, error) {
	path, err := materializeBridge()
	if err != nil {
		return nil, nil, err
	}
	stdin, err := json.Marshal(req)
	if err != nil {
		return nil, nil, fmt.Errorf("encode CAS request: %w", err)
	}
	module, haveModule := resolveSDKModule()
	logSDKOnce(module, haveModule)

	argv := []string{agent.ResolveAgentBinary(nodeBinary), path}
	extraEnv := []string(nil)
	if opts.Sandbox != nil {
		binds := append([]agent.BindMount(nil), opts.ExtraBinds...)
		binds = append(binds, agent.BindMount{Source: path, Target: path})
		if haveModule {
			// Without this the jailed child cannot import the SDK at all: the
			// global node_modules tree is outside every default bind.
			binds = append(binds, agent.BindMount{Source: module.Root, Target: module.Root})
		}
		argv, extraEnv, err = opts.Sandbox.Wrap(argv, workDir, agent.WrapOpts{ExtraBinds: binds, Unrestricted: opts.Unrestricted, JailRoot: opts.JailRoot, ConfigDir: opts.ConfigDir})
		if err != nil {
			return nil, nil, fmt.Errorf("sandbox wrap: %w", err)
		}
	}
	spawner := effectiveSpawner(opts)
	opts.Spawner = spawner
	env := streamcommon.RunEnv(opts, extraEnv, "CLAUDE_CONFIG_DIR")
	if haveModule {
		// Hand the child the absolute entry point so it never has to guess —
		// and never has to shell out to `npm root -g`, which inside a jail
		// would fail on a binary that isn't mounted.
		env = streamcommon.SetEnv(env, sdkModuleEnv, module.Entry)
	}
	runCtx, cancel := context.WithCancel(ctx)
	proc, err := agent.SpawnWithRetry(runCtx, spawner, agent.SpawnRequest{
		Argv: argv, Env: env, WorkDir: workDir, InitialStdin: string(stdin) + "\n",
		Interactive: opts.ControlID != "",
	})
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("start CAS: %w", err)
	}
	return proc, cancel, nil
}

// effectiveSpawner converts the ordinary CLI PTY layout to pipes at this
// protocol boundary, mirroring codexapp. The bridge is a programmatic NDJSON
// transport — nothing in it checks isatty — and a pty would echo the
// InitialStdin request back into stdout and merge stderr into it, losing the
// bridge's one-line failure diagnostics.
func effectiveSpawner(opts agent.RunRequest) agent.ProcessSpawner {
	if _, isPty := opts.Spawner.(*agent.PtySpawner); isPty || opts.Spawner == nil {
		return agent.PipeSpawner{}
	}
	return opts.Spawner
}

var sdkLogOnce sync.Once

// logSDKOnce records which SDK the deployment actually runs. The npm package
// carries its own Claude Code build, so this — not `claude --version` — is the
// version answering prompts on the agent-sdk transport.
func logSDKOnce(module sdkModule, ok bool) {
	sdkLogOnce.Do(func() {
		if !ok {
			log.Printf("[claude-CAS] SDK module not found on this host; the bridge will fall back to its own resolution (install with: npm install -g %s/%s)", sdkScope, sdkPackage)
			return
		}
		log.Printf("[claude-CAS] using %s/%s %s from %s", sdkScope, sdkPackage, module.Version, module.Entry)
	})
}

var bridgeMu sync.Mutex

// materializeBridge writes the embedded bridge next to a content hash of
// itself and returns the path.
//
// Content-addressed and re-checked on every run on purpose: the file lives in
// the system temp dir, where /tmp cleaners delete it out from under a
// long-running server, and two DayMug versions on one host would otherwise
// fight over a single fixed filename — one binary's bridge silently answering
// the other binary's requests.
func materializeBridge() (string, error) {
	sum := sha256.Sum256(bridgeSource)
	dir := filepath.Join(os.TempDir(), "daymug-claude-agent-sdk")
	path := filepath.Join(dir, "bridge-"+hex.EncodeToString(sum[:8])+".mjs")

	bridgeMu.Lock()
	defer bridgeMu.Unlock()
	if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Size() == int64(len(bridgeSource)) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("prepare CAS bridge: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "bridge-*.tmp")
	if err != nil {
		return "", fmt.Errorf("prepare CAS bridge: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(bridgeSource); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("prepare CAS bridge: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("prepare CAS bridge: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return "", fmt.Errorf("prepare CAS bridge: %w", err)
	}
	// Rename over any stale copy: atomic, so a concurrent run either sees the
	// old complete file or the new one, never a half-written script.
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("prepare CAS bridge: %w", err)
	}
	return path, nil
}
