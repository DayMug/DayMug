package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

func TestRunWithSessionSpeaksAppServerProtocol(t *testing.T) {
	spawner := &scriptedSpawner{}
	backend := NewBackend()
	for run := 0; run < 2; run++ {
		ch := make(chan agent.StreamEvent, 16)
		err := backend.RunWithSession(context.Background(), "hello", "/workspace", agent.RunRequest{
			Model: "gpt-5.6-terra", ConfigDir: "/codex-home", Spawner: spawner,
		}, ch)
		if err != nil {
			t.Fatalf("RunWithSession #%d: %v", run+1, err)
		}
		var kinds []string
		for event := range ch {
			kinds = append(kinds, event.Kind)
		}
		joined := strings.Join(kinds, ",")
		for _, want := range []string{agent.KindSystemInit, agent.KindDelta, agent.KindResult, agent.KindUsage} {
			if !strings.Contains(joined, want) {
				t.Fatalf("events %q missing %q", joined, want)
			}
		}
	}
	spawner.mu.Lock()
	spawnCount := spawner.spawnCount
	requests := append([]map[string]any(nil), spawner.requests...)
	argv := append([]string(nil), spawner.argv...)
	spawner.mu.Unlock()
	if spawnCount != 1 {
		t.Fatalf("spawn count = %d, want one persistent account server", spawnCount)
	}
	initializeCount := 0
	for _, request := range requests {
		if request["method"] == "initialize" {
			initializeCount++
		}
	}
	if initializeCount != 1 {
		t.Fatalf("initialize count = %d, want 1", initializeCount)
	}
	if got := strings.Join(argv, " "); !strings.Contains(got, "app-server --listen stdio://") {
		t.Fatalf("argv = %q", got)
	}
	if !containsMethod(requests, "initialize") || !containsMethod(requests, "thread/start") || !containsMethod(requests, "turn/start") {
		t.Fatalf("requests = %#v", requests)
	}
	capabilities, _ := paramsOf(t, spawner, "initialize")["capabilities"].(map[string]any)
	optOut, _ := capabilities["optOutNotificationMethods"].([]any)
	if len(optOut) != 1 || optOut[0] != "thread/started" {
		t.Fatalf("optOutNotificationMethods = %#v, want thread/started", optOut)
	}
}

func TestSteerTurnSpeaksAppServerProtocol(t *testing.T) {
	spawner := &scriptedSpawner{silentTurn: true}
	backend := NewBackend()
	steerer, ok := backend.(agent.TurnSteeringBackend)
	if !ok {
		t.Fatal("codex app-server backend does not expose turn steering")
	}

	ctx, cancel := context.WithCancel(context.Background())
	outputCh := make(chan agent.StreamEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		errCh <- backend.RunWithSession(ctx, "first", "/workspace", agent.RunRequest{
			ControlID: "conv-steer", ConfigDir: "/codex-home", Spawner: spawner,
		}, outputCh)
	}()
	waitFor(t, "active turn registration", func() bool {
		r := backend.(*runner)
		r.activeMu.RLock()
		defer r.activeMu.RUnlock()
		return r.active["conv-steer"] != nil
	})

	steerCtx, steerCancel := context.WithTimeout(context.Background(), time.Second)
	err := steerer.SteerTurn(steerCtx, "conv-steer", "message-2", "focus on tests")
	steerCancel()
	if err != nil {
		t.Fatalf("SteerTurn: %v", err)
	}
	params := paramsOf(t, spawner, "turn/steer")
	if got := params["threadId"]; got != "thread-1" {
		t.Fatalf("turn/steer threadId = %v, want thread-1", got)
	}
	if got := params["expectedTurnId"]; got != "turn-1" {
		t.Fatalf("turn/steer expectedTurnId = %v, want turn-1", got)
	}
	if got := params["clientUserMessageId"]; got != "message-2" {
		t.Fatalf("turn/steer clientUserMessageId = %v, want message-2", got)
	}
	input, _ := params["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("turn/steer input = %#v", params["input"])
	}
	item, _ := input[0].(map[string]any)
	if got := item["text"]; got != "focus on tests" {
		t.Fatalf("turn/steer text = %v", got)
	}

	cancel()
	for range outputCh {
	}
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunWithSession after cancel = %v, want context.Canceled", err)
	}
	if err := steerer.SteerTurn(context.Background(), "conv-steer", "message-3", "late"); !errors.Is(err, agent.ErrNoActiveTurn) {
		t.Fatalf("late SteerTurn = %v, want ErrNoActiveTurn", err)
	}
}

func TestAnswerUserQuestionResumesAppServerTurn(t *testing.T) {
	spawner := &scriptedSpawner{askUserQuestion: true}
	backend := NewBackend()
	responder := backend.(agent.UserQuestionBackend)
	outputCh := make(chan agent.StreamEvent, 16)
	errCh := make(chan error, 1)
	go func() {
		errCh <- backend.RunWithSession(context.Background(), "choose", "/workspace", agent.RunRequest{
			ControlID: "conv-question", EnableUserQuestions: true, ConfigDir: "/codex-home", Spawner: spawner,
		}, outputCh)
	}()

	var question agent.UserQuestionRequest
	for event := range outputCh {
		if event.Kind != agent.KindUserQuestion {
			continue
		}
		if err := json.Unmarshal([]byte(event.Content), &question); err != nil {
			t.Fatal(err)
		}
		if err := responder.AnswerUserQuestion(context.Background(), "conv-question", question.RequestID, map[string][]string{"color": {"Blue"}}); err != nil {
			t.Fatalf("AnswerUserQuestion: %v", err)
		}
	}
	if err := <-errCh; err != nil {
		t.Fatalf("RunWithSession: %v", err)
	}
	if question.RequestID != "ask-1" || len(question.Questions) != 1 || question.Questions[0].Question != "Which color?" {
		t.Fatalf("question = %+v", question)
	}

	spawner.mu.Lock()
	answer := spawner.questionAnswer
	spawner.mu.Unlock()
	result, _ := answer["result"].(map[string]any)
	answers, _ := result["answers"].(map[string]any)
	color, _ := answers["color"].(map[string]any)
	selected, _ := color["answers"].([]any)
	if len(selected) != 1 || selected[0] != "Blue" {
		t.Fatalf("provider response = %#v", answer)
	}
}

func TestPersistentServerSupportsConcurrentThreads(t *testing.T) {
	spawner := &scriptedSpawner{}
	backend := NewBackend()
	start := make(chan struct{})
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			ch := make(chan agent.StreamEvent, 16)
			err := backend.RunWithSession(context.Background(), "hello", "/workspace", agent.RunRequest{
				Model: "gpt-5.6-terra", ConfigDir: "/codex-home", Spawner: spawner,
			}, ch)
			for range ch {
			}
			errs <- err
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent run: %v", err)
		}
	}
	spawner.mu.Lock()
	spawnCount := spawner.spawnCount
	spawner.mu.Unlock()
	if spawnCount != 1 {
		t.Fatalf("spawn count = %d, want 1", spawnCount)
	}
}

func TestPersistentServersAreIsolatedByAccount(t *testing.T) {
	spawner := &scriptedSpawner{}
	backend := NewBackend()
	for _, configDir := range []string{"/accounts/one", "/accounts/two"} {
		ch := make(chan agent.StreamEvent, 16)
		if err := backend.RunWithSession(context.Background(), "hello", "/workspace", agent.RunRequest{
			ConfigDir: configDir, Spawner: spawner,
		}, ch); err != nil {
			t.Fatalf("run %s: %v", configDir, err)
		}
		for range ch {
		}
	}
	spawner.mu.Lock()
	spawnCount := spawner.spawnCount
	spawner.mu.Unlock()
	if spawnCount != 2 {
		t.Fatalf("spawn count = %d, want one per account", spawnCount)
	}
}

func TestPersistentServerSharesAcrossWorkDirsWithoutSandbox(t *testing.T) {
	spawner := &scriptedSpawner{}
	backend := NewBackend()
	sandbox, err := agent.NewSandbox(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, workDir := range []string{"/users/a", "/users/b"} {
		ch := make(chan agent.StreamEvent, 16)
		if err := backend.RunWithSession(context.Background(), "hello", workDir, agent.RunRequest{
			ConfigDir: "/account", JailRoot: workDir, Sandbox: sandbox, Spawner: spawner,
		}, ch); err != nil {
			t.Fatalf("run %s: %v", workDir, err)
		}
		for range ch {
		}
	}
	spawner.mu.Lock()
	spawnCount := spawner.spawnCount
	requests := append([]map[string]any(nil), spawner.requests...)
	spawner.mu.Unlock()
	if spawnCount != 1 {
		t.Fatalf("spawn count = %d, want one unjailed account server across work dirs", spawnCount)
	}
	seenWorkDirs := make(map[string]bool)
	for _, request := range requests {
		if request["method"] != "thread/start" {
			continue
		}
		params, _ := request["params"].(map[string]any)
		if cwd, _ := params["cwd"].(string); cwd != "" {
			seenWorkDirs[cwd] = true
		}
	}
	if !seenWorkDirs["/users/a"] || !seenWorkDirs["/users/b"] {
		t.Fatalf("thread/start cwd values = %v, want both work dirs", seenWorkDirs)
	}
}

func TestRuntimeKeyDropsInertIsolationProfile(t *testing.T) {
	sandbox, err := agent.NewSandbox(nil)
	if err != nil {
		t.Fatal(err)
	}
	base := agent.RunRequest{ConfigDir: "/account", Sandbox: sandbox, JailRoot: "/users/a"}
	other := base
	other.JailRoot = "/users/b"
	if runtimeKey(base) != runtimeKey(other) {
		t.Fatal("a no-op sandbox must not split the account server by jail root")
	}

	bypassed := agent.RunRequest{ConfigDir: "/account", Sandbox: noopSandbox{}, Unrestricted: true, JailRoot: "/users/a"}
	otherBypassed := bypassed
	otherBypassed.JailRoot = "/users/b"
	if runtimeKey(bypassed) != runtimeKey(otherBypassed) {
		t.Fatal("an unrestricted sandbox must not split the account server by jail root")
	}
}

func TestRuntimeKeyIncludesActiveIsolationProfile(t *testing.T) {
	base := agent.RunRequest{ConfigDir: "/account", Sandbox: noopSandbox{}, JailRoot: "/users/a"}
	other := base
	other.JailRoot = "/users/b"
	if runtimeKey(base) == runtimeKey(other) {
		t.Fatal("different jail roots must not share an app-server")
	}
}

func TestPoolReplacesDeadAccountServer(t *testing.T) {
	spawner := &scriptedSpawner{}
	pool := newServerPool()
	opts := agent.RunRequest{ConfigDir: "/account", Spawner: spawner}
	first, releaseFirst, err := pool.get(context.Background(), opts, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	releaseFirst()
	first.markDead(errors.New("simulated crash"))
	second, releaseSecond, err := pool.get(context.Background(), opts, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	releaseSecond()
	if first == second {
		t.Fatal("dead server was reused")
	}
	spawner.mu.Lock()
	spawnCount := spawner.spawnCount
	spawner.mu.Unlock()
	if spawnCount != 2 {
		t.Fatalf("spawn count = %d, want replacement process", spawnCount)
	}
}

func TestUnauthenticatedServerIsReplacedAfterLogin(t *testing.T) {
	spawner := &scriptedSpawner{unauthenticated: true}
	backend := NewBackend()
	opts := agent.RunRequest{ConfigDir: "/account", Spawner: spawner}

	ch := make(chan agent.StreamEvent, 16)
	if err := backend.RunWithSession(context.Background(), "hello", "/workspace", opts, ch); !errors.Is(err, errAuthenticationRequired) {
		t.Fatalf("first run error = %v, want authentication required", err)
	}

	spawner.mu.Lock()
	spawner.unauthenticated = false
	spawner.mu.Unlock()
	ch = make(chan agent.StreamEvent, 16)
	if err := backend.RunWithSession(context.Background(), "hello", "/workspace", opts, ch); err != nil {
		t.Fatalf("run after login: %v", err)
	}
	for range ch {
	}

	spawner.mu.Lock()
	spawnCount := spawner.spawnCount
	spawner.mu.Unlock()
	if spawnCount != 2 {
		t.Fatalf("spawn count = %d, want replacement after login", spawnCount)
	}
}

func containsMethod(requests []map[string]any, method string) bool {
	for _, request := range requests {
		if request["method"] == method {
			return true
		}
	}
	return false
}

type scriptedSpawner struct {
	mu              sync.Mutex
	argv            []string
	requests        []map[string]any
	spawnCount      int
	threadCount     int
	unauthenticated bool
	// resumeFails makes thread/resume answer with a protocol error, the shape
	// Codex returns for a thread id it can no longer load.
	resumeFails bool
	// resumeDropsTransport makes the first process exit while handling
	// thread/resume, reproducing a reader failure that makes that client
	// unusable for the fresh-thread fallback.
	resumeDropsTransport bool
	// silentTurn accepts turn/start and then emits nothing, the shape a wedged
	// upstream takes: no notifications, no completion, no exit.
	silentTurn bool
	// streamRetries narrates that many retryable "error" notifications before
	// the turn's real output, the shape codex takes while reconnecting a
	// dropped response stream.
	streamRetries   int
	askUserQuestion bool
	questionAnswer  map[string]any
	// turnStartGate, when non-nil, holds the turn/start response (and the rest
	// of the serve loop) until the channel is closed — the window in which a
	// user cancel used to orphan the just-started turn.
	turnStartGate chan struct{}
}

func (s *scriptedSpawner) Spawn(_ context.Context, req agent.SpawnRequest) (agent.RunningProcess, error) {
	s.mu.Lock()
	s.argv = append([]string(nil), req.Argv...)
	s.spawnCount++
	spawnNumber := s.spawnCount
	s.mu.Unlock()
	clientReader, clientWriter := io.Pipe()
	serverReader, serverWriter := io.Pipe()
	p := &scriptedProcess{stdin: clientWriter, stdout: serverReader, done: make(chan struct{})}
	go s.serve(clientReader, serverWriter, p.done, spawnNumber)
	return p, nil
}

func (s *scriptedSpawner) serve(in io.Reader, out *io.PipeWriter, done chan struct{}, spawnNumber int) {
	defer close(done)
	defer func() { _ = out.Close() }()
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		var request map[string]any
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		s.mu.Lock()
		s.requests = append(s.requests, request)
		s.mu.Unlock()
		method, _ := request["method"].(string)
		id, hasID := request["id"]
		if method == "" && fmt.Sprint(id) == "ask-1" {
			s.mu.Lock()
			s.questionAnswer = request
			threadID := fmt.Sprintf("thread-%d", s.threadCount)
			s.mu.Unlock()
			writeJSON(out, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": threadID, "turn": map[string]any{"id": "turn-1", "status": "completed"}}})
			continue
		}
		if !hasID {
			continue
		}
		switch method {
		case "initialize":
			writeJSON(out, map[string]any{"id": id, "result": map[string]any{}})
		case "account/read":
			s.mu.Lock()
			unauthenticated := s.unauthenticated
			s.mu.Unlock()
			if unauthenticated {
				writeJSON(out, map[string]any{"id": id, "result": map[string]any{"account": nil, "requiresOpenaiAuth": true}})
			} else {
				writeJSON(out, map[string]any{"id": id, "result": map[string]any{"account": map[string]any{"type": "chatgpt"}, "requiresOpenaiAuth": true}})
			}
		case "thread/start":
			s.mu.Lock()
			s.threadCount++
			threadID := fmt.Sprintf("thread-%d", s.threadCount)
			s.mu.Unlock()
			writeJSON(out, map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": threadID}}})
		case "thread/resume":
			s.mu.Lock()
			resumeFails := s.resumeFails
			resumeDropsTransport := s.resumeDropsTransport
			s.mu.Unlock()
			if resumeDropsTransport && spawnNumber == 1 {
				return
			}
			if resumeFails {
				writeJSON(out, map[string]any{"id": id, "error": map[string]any{"code": -32603, "message": "thread not found"}})
				continue
			}
			params, _ := request["params"].(map[string]any)
			threadID, _ := params["threadId"].(string)
			writeJSON(out, map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": threadID}}})
		case "turn/interrupt":
			writeJSON(out, map[string]any{"id": id, "result": map[string]any{}})
		case "turn/steer":
			writeJSON(out, map[string]any{"id": id, "result": map[string]any{"turnId": "turn-1"}})
		case "turn/start":
			s.mu.Lock()
			gate := s.turnStartGate
			s.mu.Unlock()
			if gate != nil {
				<-gate
			}
			params, _ := request["params"].(map[string]any)
			threadID, _ := params["threadId"].(string)
			writeJSON(out, map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": "turn-1"}}})
			s.mu.Lock()
			silent := s.silentTurn
			retries := s.streamRetries
			askQuestion := s.askUserQuestion
			s.mu.Unlock()
			if silent {
				continue
			}
			for attempt := 1; attempt <= retries; attempt++ {
				writeJSON(out, map[string]any{"method": "error", "params": map[string]any{
					"threadId": threadID, "turnId": "turn-1", "willRetry": true,
					"error": map[string]any{
						"message":        fmt.Sprintf("Reconnecting... %d/%d", attempt, retries),
						"codexErrorInfo": map[string]any{"responseStreamDisconnected": map[string]any{"httpStatusCode": 500}},
					},
				}})
			}
			if askQuestion {
				writeJSON(out, map[string]any{
					"id": "ask-1", "method": "item/tool/requestUserInput",
					"params": map[string]any{
						"threadId": threadID, "turnId": "turn-1",
						"questions": []map[string]any{{
							"id": "color", "header": "Color", "question": "Which color?", "isOther": true,
							"options": []map[string]string{{"label": "Blue", "description": "Calm"}},
						}},
					},
				})
				continue
			}
			writeJSON(out, map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": threadID, "delta": "hello"}})
			writeJSON(out, map[string]any{"method": "item/completed", "params": map[string]any{"threadId": threadID, "item": map[string]any{"id": "msg-1", "type": "agentMessage", "text": "hello"}}})
			writeJSON(out, map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": threadID, "tokenUsage": map[string]any{"last": map[string]any{"inputTokens": 10, "cachedInputTokens": 0, "outputTokens": 2, "reasoningOutputTokens": 0, "totalTokens": 12}}}})
			writeJSON(out, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": threadID, "turn": map[string]any{"id": "turn-1", "status": "completed"}}})
		case "thread/unsubscribe":
			writeJSON(out, map[string]any{"id": id, "result": map[string]any{}})
		}
	}
}

func writeJSON(w io.Writer, value any) {
	b, _ := json.Marshal(value)
	_, _ = w.Write(append(b, '\n'))
}

type scriptedProcess struct {
	stdin  io.WriteCloser
	stdout io.Reader
	done   chan struct{}
}

func (p *scriptedProcess) Stdout() io.Reader        { return p.stdout }
func (p *scriptedProcess) Stderr() io.Reader        { return nil }
func (p *scriptedProcess) Stdin() io.WriteCloser    { return p.stdin }
func (p *scriptedProcess) Wait() error              { <-p.done; return nil }
func (p *scriptedProcess) Signal(_ os.Signal) error { return nil }
