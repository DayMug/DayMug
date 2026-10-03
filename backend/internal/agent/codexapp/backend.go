// Package codexapp adapts a pool of account-scoped Codex app-server processes
// to DayMug's provider-neutral agent.Backend stream.
package codexapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/sessionlog"
	"github.com/DayMug/DayMug/backend/internal/agent/streamcommon"
	"github.com/DayMug/DayMug/backend/internal/config"
)

var codexBinary = "codex"

type runner struct {
	pool     *serverPool
	activeMu sync.RWMutex
	active   map[string]*activeTurn
	// provider is DayMug's CLIType for this Backend instance ("codex" or
	// "openai-compatible"). The wire protocol is identical — the difference
	// is which endpoint the account's env points the app-server at — but the
	// price table is not shared: a third-party endpoint's per-token rates
	// have nothing to do with OpenAI's, even for a same-named model id.
	provider string
}

type activeTurn struct {
	server           *accountServer
	threadID         string
	turnID           string
	pendingQuestions map[string]json.RawMessage
}

func NewBackend() agent.Backend {
	return NewBackendForProvider(config.CLITypeCodex)
}

// NewBackendForProvider returns an app-server Backend bound to a specific
// DayMug CLIType, so the same transport can serve both the first-party codex
// login and an openai-compatible endpoint without their costs cross-billing.
func NewBackendForProvider(provider string) agent.Backend {
	pool := newServerPool()
	pool.provider = provider
	r := &runner{pool: pool, active: make(map[string]*activeTurn), provider: provider}
	agent.RegisterCloser(r.pool.closeAll)
	return r
}

func (r *runner) Name() string {
	if r.provider != "" && r.provider != config.CLITypeCodex {
		return r.provider + "-app-server"
	}
	return "codex-app-server"
}

// Capabilities mirrors codexcli on everything the user can act on: the
// transport an operator picked must not change which slash commands the UI
// offers on a Codex conversation. TestCapabilitiesMatchTheCliTransport is what
// keeps the two honest.
//
// ReportsContextUsage is the one deliberate divergence. `thread/tokenUsage/updated`
// carries modelContextWindow, so this transport can report a real context
// occupancy; the CLI's `token_count` envelope cannot (see codexcli's
// Capabilities). Reporting it per-transport is what lets the UI show the bar
// here and hide it there.
func (*runner) Capabilities() agent.Capabilities {
	return agent.Capabilities{
		SupportsCompaction:     true,
		SupportsThinkingStream: true,
		ReportsContextUsage:    true,
		AssignsSessionID:       true,
	}
}

// SessionExists and SessionLogPath read the rollout JSONL codex writes
// whether the turn arrived over `codex exec` or over the app-server, so the
// retry path can rewind a failed attempt before trying it again.
func (r *runner) SessionExists(workDir, sessionID, configDir string) bool {
	return sessionlog.Codex{}.SessionExists(workDir, sessionID, configDir)
}

func (r *runner) SessionLogPath(workDir, sessionID, configDir string) string {
	return sessionlog.Codex{}.SessionLogPath(workDir, sessionID, configDir)
}

func (r *runner) RunOneshot(ctx context.Context, prompt, workDir string, opts agent.RunRequest) (string, error) {
	ch := make(chan agent.StreamEvent, 32)
	errCh := make(chan error, 1)
	go func() { errCh <- r.RunWithSession(ctx, prompt, workDir, opts, ch) }()
	return streamcommon.DrainOneshot(ch, errCh)
}

func (r *runner) RunWithSession(ctx context.Context, prompt, workDir string, opts agent.RunRequest, outputCh chan<- agent.StreamEvent) error {
	defer close(outputCh)
	srv, release, err := r.pool.get(ctx, opts, workDir)
	if err != nil {
		return err
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	if err := checkAuthentication(ctx, srv.client); err != nil {
		if errors.Is(err, errAuthenticationRequired) {
			r.pool.invalidate(opts, srv, err)
		}
		return err
	}

	opened, err := openThread(ctx, srv.client, workDir, opts)
	if err != nil && opts.SessionID != "" && opts.IsResume && ctx.Err() == nil && !errors.Is(err, errAuthenticationRequired) && srv.client.stopped() {
		resumeErr := err
		log.Printf("[codex-CAS] resume of thread %s lost its server (%v); replacing it before starting a fresh thread", opts.SessionID, err)
		r.pool.invalidate(opts, srv, err)
		release()
		release = nil

		srv, release, err = r.pool.get(ctx, opts, workDir)
		if err != nil {
			return fmt.Errorf("replace CAS after failed thread resume: %w", err)
		}
		if err = checkAuthentication(ctx, srv.client); err != nil {
			if errors.Is(err, errAuthenticationRequired) {
				r.pool.invalidate(opts, srv, err)
			}
			return err
		}
		fresh := opts
		fresh.SessionID = ""
		opened.ThreadID, err = startThread(ctx, srv.client, "thread/start", workDir, fresh)
		opened.PreviousThreadID = opts.SessionID
		opened.ResumeErr = resumeErr
	}
	if err != nil {
		if errors.Is(err, errAuthenticationRequired) {
			r.pool.invalidate(opts, srv, err)
		}
		return err
	}
	// Every write to outputCh from here down goes through the emitter: it
	// bounds the send so an abandoned consumer fails this turn instead of
	// wedging the loop below — which is also the loop that services the stall
	// watchdog, so a bare send would block the one mechanism meant to catch it.
	em := streamcommon.NewEmitter(ctx, outputCh, "codex CAS")
	if err := em.Emit(sessionDiagnostic(opened)); err != nil {
		return err
	}
	threadID := opened.ThreadID
	if err := em.Emit(systemInit(threadID, opts.Model)); err != nil {
		return err
	}
	sub, unsubscribe := srv.subscribe(threadID)
	defer func() {
		unsubscribe()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.client.call(cleanupCtx, "thread/unsubscribe", map[string]any{"threadId": threadID}, nil)
		cancel()
	}()

	params := map[string]any{
		"threadId": threadID, "input": []map[string]string{{"type": "text", "text": prompt}},
		"cwd": workDir, "approvalPolicy": "never", "sandboxPolicy": sandboxPolicy(opts),
	}
	if opts.Model != "" {
		params["model"] = opts.Model
	}
	if opts.ThinkLevel != "" {
		effort := opts.ThinkLevel
		if effort == "max" {
			effort = "xhigh"
		}
		params["effort"] = effort
	}
	turnID, err := r.startTurn(ctx, srv, threadID, params)
	if err != nil {
		if errors.Is(err, errAuthenticationRequired) {
			r.pool.invalidate(opts, srv, err)
		}
		return fmt.Errorf("start CAS turn: %w", err)
	}
	_, clearActive := r.registerActiveTurn(opts.ControlID, srv, threadID, turnID)
	defer clearActive()

	return r.pumpTurn(ctx, srv, sub, threadID, turnID, workDir, opts, em)
}

func (r *runner) registerActiveTurn(controlID string, srv *accountServer, threadID, turnID string) (*activeTurn, func()) {
	if controlID == "" {
		return nil, func() {}
	}
	active := &activeTurn{server: srv, threadID: threadID, turnID: turnID, pendingQuestions: make(map[string]json.RawMessage)}
	r.activeMu.Lock()
	r.active[controlID] = active
	r.activeMu.Unlock()
	return active, func() {
		r.activeMu.Lock()
		if r.active[controlID] == active {
			delete(r.active, controlID)
		}
		r.activeMu.Unlock()
	}
}

// AnswerUserQuestion resolves an app-server request emitted by the active
// turn. The stored raw JSON-RPC id is used for the response so both numeric
// and string ids round-trip exactly.
func (r *runner) AnswerUserQuestion(ctx context.Context, controlID, requestID string, answers map[string][]string) error {
	r.activeMu.Lock()
	active := r.active[controlID]
	var rawID json.RawMessage
	if active != nil {
		rawID = active.pendingQuestions[requestID]
		delete(active.pendingQuestions, requestID)
	}
	r.activeMu.Unlock()
	if active == nil || len(rawID) == 0 {
		return agent.ErrNoActiveQuestion
	}

	encodedAnswers := make(map[string]map[string][]string, len(answers))
	for id, selected := range answers {
		encodedAnswers[id] = map[string][]string{"answers": selected}
	}
	if err := active.server.client.respondResult(rawID, map[string]any{"answers": encodedAnswers}); err != nil {
		r.activeMu.Lock()
		if r.active[controlID] == active {
			active.pendingQuestions[requestID] = rawID
		}
		r.activeMu.Unlock()
		return fmt.Errorf("answer CAS user question: %w", err)
	}
	return nil
}

// SteerTurn appends input to the regular turn currently registered for the
// caller's control id. expectedTurnId makes completion/steer races fail closed
// at app-server instead of accidentally targeting a later turn.
func (r *runner) SteerTurn(ctx context.Context, controlID, messageID, input string) error {
	r.activeMu.RLock()
	active := r.active[controlID]
	r.activeMu.RUnlock()
	if active == nil {
		return agent.ErrNoActiveTurn
	}
	params := map[string]any{
		"threadId":       active.threadID,
		"expectedTurnId": active.turnID,
		"input":          []map[string]string{{"type": "text", "text": input}},
	}
	if messageID != "" {
		params["clientUserMessageId"] = messageID
	}
	if err := active.server.client.call(ctx, "turn/steer", params, nil); err != nil {
		return fmt.Errorf("steer CAS turn: %w", err)
	}
	return nil
}

// startTurnTimeout bounds the turn/start round-trip. The server answers as
// soon as the turn is queued, so this is generous; it exists because the RPC
// below runs on a context detached from the caller's.
const startTurnTimeout = 30 * time.Second

// startTurn issues turn/start and returns the new turn's id.
//
// The RPC is raced against ctx by hand instead of being passed ctx directly:
// once the request bytes are on the wire the server may start the turn whether
// or not we stay for the answer, and rpcClient.call simply abandons the
// pending response on cancellation. A cancel in that window used to leave an
// orphan turn running server-side — burning tokens and appending to the
// rollout — with nobody left to interrupt it. Waiting the response out in the
// background (bounded by startTurnTimeout, detached from ctx) recovers the
// turn id so the orphan can be interrupted.
func (r *runner) startTurn(ctx context.Context, srv *accountServer, threadID string, params map[string]any) (string, error) {
	type outcome struct {
		turnID string
		err    error
	}
	callCtx, cancelCall := context.WithTimeout(context.Background(), startTurnTimeout)
	resultCh := make(chan outcome, 1)
	go func() {
		var started struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		err := srv.client.call(callCtx, "turn/start", params, &started)
		resultCh <- outcome{turnID: started.Turn.ID, err: err}
	}()
	select {
	case res := <-resultCh:
		cancelCall()
		return res.turnID, res.err
	case <-ctx.Done():
		go func() {
			defer cancelCall()
			if res := <-resultCh; res.err == nil {
				r.interrupt(srv, threadID, res.turnID)
			}
		}()
		return "", ctx.Err()
	}
}

// pumpTurn forwards one turn's notifications and owns the turn's liveness.
//
// The watchdog is the same streamcommon.Watchdog the pipe adapters' LineStream
// runs — StallTimeout with an activity-probe reprieve, MaxSilentTimeout as the
// absolute cap. On a stall the turn is interrupted before the error returns, so
// the app-server stops spending on an answer nobody will read.
func (r *runner) pumpTurn(
	ctx context.Context,
	srv *accountServer,
	sub *subscription,
	threadID, turnID, workDir string,
	opts agent.RunRequest,
	em *streamcommon.Emitter,
) error {
	watchdog := streamcommon.NewWatchdog(streamcommon.WatchdogConfig{
		Source:           "codex CAS",
		StallTimeout:     opts.StallTimeout,
		MaxSilentTimeout: opts.MaxSilentTimeout,
		ActivityProbe: agent.NewProcessActivityProbe(srv.proc, func() []string {
			if opts.SessionID == "" {
				return nil
			}
			return []string{r.SessionLogPath(workDir, opts.SessionID, opts.ConfigDir)}
		}),
		// Read through the package seam at call time so a test that swaps
		// nowFunc after the turn starts still drives the silence clock.
		Now: func() time.Time { return nowFunc() },
	})
	defer watchdog.Stop()

	var pendingResult *agent.StreamEvent
	for {
		select {
		case <-ctx.Done():
			r.interrupt(srv, threadID, turnID)
			return ctx.Err()
		case <-srv.done:
			return srv.failure()
		case <-sub.lost:
			// Either the server retired (done fires with it, and the race
			// between the two is harmless) or this turn's queue overflowed and
			// the stream now has a hole in it. Both are fatal for the turn;
			// reporting a truncated answer as complete is worse than failing.
			if !srv.alive() {
				return srv.failure()
			}
			r.interrupt(srv, threadID, turnID)
			return fmt.Errorf("%w for thread %s", errEventBacklogOverflow, threadID)
		case <-watchdog.C():
			if err := watchdog.Expired(); err != nil {
				r.interrupt(srv, threadID, turnID)
				return err
			}
		case msg := <-sub.ch:
			watchdog.Activity()
			if len(msg.ID) > 0 && msg.Method == "item/tool/requestUserInput" {
				if err := r.forwardUserQuestion(srv, msg, opts, em); err != nil {
					return err
				}
				continue
			}
			if msg.Method == "serverRequest/resolved" {
				if requestID := r.resolveUserQuestion(opts.ControlID, msg.Params); requestID != "" {
					if err := em.Emit(agent.StreamEvent{Kind: agent.KindUserQuestionResolved, Content: requestID}); err != nil {
						return err
					}
				}
				continue
			}
			if msg.Method == usageNotification && !srv.acceptUsage(threadID, turnID, msg.Params) {
				// A turn interrupted before it spent anything still gets a
				// usage report, restating the previous turn's figures; billing
				// them again charges the account twice for one answer.
				log.Printf("[codex-CAS] thread %s turn %s: ignoring a usage report that belongs to an earlier turn", threadID, turnID)
				continue
			}
			mapped, completed, eventErr := mapNotification(r.provider, msg, opts.Model)
			if errors.Is(eventErr, errStreamRetry) {
				log.Printf("[codex-CAS] thread %s: %v", threadID, eventErr)
				continue
			}
			if errors.Is(eventErr, errAuthenticationRequired) {
				r.pool.invalidate(opts, srv, eventErr)
			}
			for _, evt := range mapped {
				if evt.Kind == agent.KindResult {
					// Codex can complete several agentMessage items in one turn
					// with no usage frame between them; flush the held chunk
					// before replacing it — the streamer downstream merges
					// multiple result events, but it can't merge one we dropped.
					if pendingResult != nil {
						if err := em.Emit(*pendingResult); err != nil {
							return err
						}
					}
					held := evt
					pendingResult = &held
					continue
				}
				if err := em.Emit(evt); err != nil {
					return err
				}
				if evt.Kind == agent.KindUsage && pendingResult != nil {
					if err := em.Emit(*pendingResult); err != nil {
						return err
					}
					pendingResult = nil
				}
			}
			if completed {
				if pendingResult != nil {
					if err := em.Emit(*pendingResult); err != nil {
						return err
					}
				}
				return eventErr
			}
			if eventErr != nil {
				return eventErr
			}
		}
	}
}

func (r *runner) resolveUserQuestion(controlID string, raw json.RawMessage) string {
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return ""
	}
	requestID, err := displayRequestID(params.RequestID)
	if err != nil {
		return ""
	}
	r.activeMu.Lock()
	defer r.activeMu.Unlock()
	active := r.active[controlID]
	if active == nil || len(active.pendingQuestions[requestID]) == 0 {
		return ""
	}
	delete(active.pendingQuestions, requestID)
	return requestID
}

type appServerQuestionParams struct {
	ThreadID         string `json:"threadId"`
	TurnID           string `json:"turnId"`
	AutoResolutionMS *int64 `json:"autoResolutionMs"`
	Questions        []struct {
		ID       string `json:"id"`
		Header   string `json:"header"`
		Question string `json:"question"`
		IsOther  bool   `json:"isOther"`
		IsSecret bool   `json:"isSecret"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"questions"`
}

func (r *runner) forwardUserQuestion(srv *accountServer, msg wireMessage, opts agent.RunRequest, em *streamcommon.Emitter) error {
	r.activeMu.RLock()
	active := r.active[opts.ControlID]
	r.activeMu.RUnlock()
	if !opts.EnableUserQuestions || active == nil {
		_ = srv.client.respondError(msg.ID, -32601, "interactive questions are unavailable for this DayMug client")
		return nil
	}
	var params appServerQuestionParams
	if err := json.Unmarshal(msg.Params, &params); err != nil || len(params.Questions) == 0 {
		_ = srv.client.respondError(msg.ID, -32602, "invalid user question request")
		return nil
	}
	requestID, err := displayRequestID(msg.ID)
	if err != nil {
		_ = srv.client.respondError(msg.ID, -32602, "invalid request id")
		return nil
	}
	normalized := agent.UserQuestionRequest{RequestID: requestID, AutoResolutionMS: params.AutoResolutionMS}
	for _, q := range params.Questions {
		question := agent.UserQuestion{ID: q.ID, Header: q.Header, Question: q.Question, AllowOther: q.IsOther, Secret: q.IsSecret}
		for _, option := range q.Options {
			question.Options = append(question.Options, agent.UserQuestionOption{Label: option.Label, Description: option.Description})
		}
		normalized.Questions = append(normalized.Questions, question)
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("encode CAS user question: %w", err)
	}
	r.activeMu.Lock()
	if r.active[opts.ControlID] != active {
		r.activeMu.Unlock()
		_ = srv.client.respondError(msg.ID, -32602, "turn is no longer active")
		return nil
	}
	active.pendingQuestions[requestID] = append(json.RawMessage(nil), msg.ID...)
	r.activeMu.Unlock()
	return em.Emit(agent.StreamEvent{Kind: agent.KindUserQuestion, Content: string(payload)})
}

func displayRequestID(raw json.RawMessage) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	switch id := value.(type) {
	case string:
		return id, nil
	case float64:
		return fmt.Sprintf("%.0f", id), nil
	default:
		return "", fmt.Errorf("unsupported JSON-RPC id")
	}
}

// interrupt asks Codex to abandon the turn. Best-effort by design: the caller
// is already returning an error, and a server that has stopped answering
// notifications may well not answer this either.
func (r *runner) interrupt(srv *accountServer, threadID, turnID string) {
	if turnID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.client.call(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil)
}

type threadOpenResult struct {
	ThreadID         string
	PreviousThreadID string
	Resumed          bool
	ResumeErr        error
}

// openThread resumes the conversation's Codex thread, falling back to a fresh
// one when that fails. It preserves the failed transition in the result so the
// caller can tell the user that the visible transcript and model context have
// diverged.
//
// The fallback is the difference between "your history is gone" and "your
// conversation is dead": a thread id can outlive the rollout Codex would need
// to reload it (pruned history, a CODEX_HOME restored from backup, an id
// written by another machine), and before this the resume error propagated
// straight to the user on every subsequent turn with no way out but a new
// conversation.
func openThread(ctx context.Context, client *rpcClient, workDir string, opts agent.RunRequest) (threadOpenResult, error) {
	if opts.SessionID != "" && opts.IsResume {
		threadID, err := startThread(ctx, client, "thread/resume", workDir, opts)
		if err == nil {
			return threadOpenResult{ThreadID: threadID, PreviousThreadID: opts.SessionID, Resumed: true}, nil
		}
		if errors.Is(err, errAuthenticationRequired) || ctx.Err() != nil {
			return threadOpenResult{}, err
		}
		if client.stopped() {
			return threadOpenResult{}, err
		}
		log.Printf("[codex-CAS] resume of thread %s failed (%v); starting a fresh thread", opts.SessionID, err)
		fresh := opts
		fresh.SessionID = ""
		threadID, startErr := startThread(ctx, client, "thread/start", workDir, fresh)
		if startErr != nil {
			return threadOpenResult{}, startErr
		}
		return threadOpenResult{ThreadID: threadID, PreviousThreadID: opts.SessionID, ResumeErr: err}, nil
	}
	fresh := opts
	fresh.SessionID = ""
	threadID, err := startThread(ctx, client, "thread/start", workDir, fresh)
	return threadOpenResult{ThreadID: threadID}, err
}

func sessionDiagnostic(opened threadOpenResult) agent.StreamEvent {
	if opened.ResumeErr != nil {
		return agent.StreamEvent{Kind: agent.KindSessionWarning, Content: fmt.Sprintf(
			"CAS could not resume thread %s and started a fresh thread %s. Earlier chat remains visible but is no longer in the model context. Reason: %s",
			opened.PreviousThreadID, opened.ThreadID, compactDiagnosticError(opened.ResumeErr),
		)}
	}
	if opened.Resumed {
		return agent.StreamEvent{Kind: agent.KindSessionInfo, Content: "CAS resumed thread " + opened.ThreadID}
	}
	return agent.StreamEvent{Kind: agent.KindSessionInfo, Content: "CAS started thread " + opened.ThreadID}
}

func compactDiagnosticError(err error) string {
	text := strings.Join(strings.Fields(err.Error()), " ")
	runes := []rune(text)
	const maxRunes = 300
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return text
}

func startThread(ctx context.Context, client *rpcClient, method, workDir string, opts agent.RunRequest) (string, error) {
	params := map[string]any{"cwd": workDir, "approvalPolicy": "never", "sandbox": sandboxMode(opts), "serviceName": "daymug"}
	if opts.SessionID != "" {
		params["threadId"] = opts.SessionID
	}
	if opts.Model != "" {
		params["model"] = opts.Model
	}
	if opts.ContextWindow > 0 {
		params["config"] = map[string]any{"model_context_window": opts.ContextWindow}
	}
	if opts.SystemPrompt != "" && !opts.CodexMinimalContext {
		params["developerInstructions"] = opts.SystemPrompt
	}
	if opts.CodexMinimalContext && opts.SessionID == "" {
		params["ephemeral"] = true
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := client.call(ctx, method, params, &response); err != nil {
		return "", fmt.Errorf("%s: %w", method, err)
	}
	if response.Thread.ID == "" {
		return "", fmt.Errorf("%s returned an empty thread id", method)
	}
	return response.Thread.ID, nil
}

var errAuthenticationRequired = errors.New("codex authentication required")

func checkAuthentication(ctx context.Context, client *rpcClient) error {
	var response struct {
		Account            json.RawMessage `json:"account"`
		RequiresOpenAIAuth bool            `json:"requiresOpenaiAuth"`
	}
	if err := client.call(ctx, "account/read", map[string]any{"refreshToken": false}, &response); err != nil {
		return fmt.Errorf("read CAS account: %w", err)
	}
	if response.RequiresOpenAIAuth && (len(response.Account) == 0 || string(response.Account) == "null") {
		return errAuthenticationRequired
	}
	return nil
}

// systemInit reports what the transport knows at thread open: the session id
// the conversation must resume with, and the model. No tool list — the
// app-server doesn't publish one at thread/start, and inventing a placeholder
// would render as a wrong "Tools: N available" in the UI. Matches codexcli.
func systemInit(threadID, model string) agent.StreamEvent {
	payload := map[string]any{"session_id": threadID}
	if model != "" {
		payload["model"] = model
	}
	b, _ := json.Marshal(payload)
	return agent.StreamEvent{Kind: agent.KindSystemInit, Content: string(b)}
}

var errTurnInterrupted = errors.New("codex turn interrupted")
