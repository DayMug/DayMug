package imbridge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/imbot/imbottest"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func TestNewIMRunContextDeadline(t *testing.T) {
	tests := []struct {
		name         string
		timeout      time.Duration
		wantDeadline bool
	}{
		{name: "disabled by default", timeout: 0, wantDeadline: false},
		{name: "configured limit", timeout: 45 * time.Minute, wantDeadline: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := newIMRunContext(context.Background(), tt.timeout)
			defer cancel()
			deadline, ok := ctx.Deadline()
			if ok != tt.wantDeadline {
				t.Fatalf("deadline present = %v, want %v", ok, tt.wantDeadline)
			}
			if ok {
				remaining := time.Until(deadline)
				if remaining <= 44*time.Minute || remaining > tt.timeout {
					t.Fatalf("deadline remaining = %v, want approximately %v", remaining, tt.timeout)
				}
			}
		})
	}
}

func TestConversationWindowExpiredUsesFixedCreationBoundary(t *testing.T) {
	start := time.Date(2026, time.July, 28, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		createdAt   time.Time
		duration    string
		now         time.Time
		wantExpired bool
		wantErr     bool
	}{
		{name: "inside default window", createdAt: start, now: start.Add(12*time.Hour - time.Nanosecond)},
		{name: "at default boundary", createdAt: start, now: start.Add(12 * time.Hour), wantExpired: true},
		{name: "explicitly unlimited", createdAt: start, duration: "0", now: start.Add(24 * time.Hour)},
		{name: "inside fixed window", createdAt: start, duration: "3h", now: start.Add(3*time.Hour - time.Nanosecond)},
		{name: "at fixed boundary", createdAt: start, duration: "3h", now: start.Add(3 * time.Hour), wantExpired: true},
		{name: "past fixed boundary", createdAt: start, duration: "3h", now: start.Add(4 * time.Hour), wantExpired: true},
		{name: "missing creation timestamp", duration: "3h", now: start.Add(4 * time.Hour)},
		{name: "invalid duration", createdAt: start, duration: "forever", now: start, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expired, err := conversationWindowExpired(tt.createdAt, tt.duration, tt.now)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, want error %v", err, tt.wantErr)
			}
			if expired != tt.wantExpired {
				t.Fatalf("expired = %v, want %v", expired, tt.wantExpired)
			}
		})
	}
}

// The imRun phase tests below drive acquireSlot and execute directly. Both were
// previously reachable only through HandleMessage, which meant the orchestration
// they own — acquisition ordering, reverse-order release, status-message
// sequencing, and the persistence that must survive an interrupted turn — could
// only be asserted indirectly.

const (
	slotTestAccount        = "acc1"
	slotTestModel          = "claude-run-model"
	slotTestConversationID = "conv1"
)

// newSlotTestRun assembles the imRun state that resolveTarget +
// prepareConversation would have produced, so the later phases can be driven on
// their own.
func newSlotTestRun(t *testing.T, maxConcurrent int) (*imRun, *service.Pool, *imbottest.ReplyRecorder) {
	t.Helper()
	useAccountModels(t, map[string][]string{slotTestAccount: {slotTestModel}})
	cfg := &config.Config{Providers: []config.Provider{{
		Name: slotTestAccount, Type: config.CLITypeClaude,
		MaxConcurrent: maxConcurrent,
	}}}
	pool := service.NewPool(cfg)
	recorder := &imbottest.ReplyRecorder{}
	bridge := &IMBridge{
		Runtime: &service.Runtime{Store: storetest.New(), Cfg: cfg, Pool: pool, Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub()},
	}
	return &imRun{
		bridge:    bridge,
		msg:       imbot.Message{Platform: imbot.PlatformSlack, ChannelID: "C1", ThreadID: "T1", MessageID: "m1", Text: "部署一下"},
		responder: recorder,
		imTarget: imTarget{
			agentUser: store.User{ID: "agent1", Name: "Ops", OwnerID: "owner1", WorkDir: t.TempDir()},
			provider:  config.CLITypeClaude,
			model:     slotTestModel,
			boundName: slotTestAccount,
			account:   &cfg.Providers[0],
		},
		imMirror: imMirror{conversation: store.Conversation{
			ID: slotTestConversationID, UserID: "agent1", Provider: config.CLITypeClaude, Model: slotTestModel,
		}},
	}, pool, recorder
}

// holdOnlySlot occupies the single-slot account so the next entrant queues.
func holdOnlySlot(t *testing.T, pool *service.Pool) *service.Ticket {
	t.Helper()
	holder, err := pool.EnterForUser("", slotTestAccount, slotTestModel)
	if err != nil {
		t.Fatalf("hold slot: %v", err)
	}
	if err := holder.Wait(context.Background()); err != nil {
		t.Fatalf("hold slot wait: %v", err)
	}
	return holder
}

func waitForPoolSlot(t *testing.T, pool *service.Pool) {
	t.Helper()
	ticket, err := pool.EnterForUser("", slotTestAccount, slotTestModel)
	if err != nil {
		t.Fatalf("re-enter pool: %v", err)
	}
	defer ticket.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ticket.Wait(ctx); err != nil {
		t.Fatalf("account slot was never returned to the pool: %v", err)
	}
}

func TestAcquireSlotAcknowledgesThenRunsAndReleasesInReverse(t *testing.T) {
	r, pool, recorder := newSlotTestRun(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release, _, err := r.acquireSlotWithGate(ctx, cancel)
	if err != nil {
		t.Fatalf("acquireSlot: %v", err)
	}
	// The banner is posted off the critical path, so the transcript is only
	// settled once the announcer is done — the same join run() does.
	r.announce.wait()

	if got := recorder.All(); len(got) != 2 || got[0] != AckText || got[1] != thinkingStatusText {
		t.Fatalf("thread updates = %q, want the ack followed by the thinking banner", got)
	}
	jobs := r.bridge.Drainer.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("drainer jobs = %+v, want exactly one", jobs)
	}
	if jobs[0].Status != service.JobStatusRunning {
		t.Fatalf("drainer job status = %q, want %q once the pool granted the slot", jobs[0].Status, service.JobStatusRunning)
	}
	// The admin view groups by the owning login user, not by the agent row.
	if jobs[0].UserID != "owner1" || jobs[0].ConversationID != slotTestConversationID {
		t.Fatalf("drainer job = %+v, want it attributed to the owner and the mirrored conversation", jobs[0])
	}
	if r.bridge.Broadcaster.StartJob(slotTestConversationID, func() {}) {
		t.Fatal("Broadcaster accepted a second job for a conversation that is already running")
	}

	release()

	if n := r.bridge.Drainer.InFlight(); n != 0 {
		t.Fatalf("drainer in-flight after release = %d, want 0", n)
	}
	if !r.bridge.Broadcaster.StartJob(slotTestConversationID, func() {}) {
		t.Fatal("release did not end the Broadcaster job; the conversation stays busy forever")
	}
	r.bridge.Broadcaster.EndJob(slotTestConversationID)
	waitForPoolSlot(t, pool)
}

func TestAcquireSlotWithoutOptionalCoordinators(t *testing.T) {
	r, pool, recorder := newSlotTestRun(t, 1)
	r.bridge.Broadcaster = nil
	r.bridge.Drainer = nil

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, _, err := r.acquireSlotWithGate(ctx, cancel)
	if err != nil {
		t.Fatalf("acquireSlot without Broadcaster/Drainer: %v", err)
	}
	r.announce.wait()
	release()

	if got := recorder.All(); len(got) != 2 || got[0] != AckText || got[1] != thinkingStatusText {
		t.Fatalf("thread updates = %q, want the ack followed by the thinking banner", got)
	}
	waitForPoolSlot(t, pool)
}

func TestAcquireSlotRefusesWhenConversationAlreadyBusy(t *testing.T) {
	r, pool, recorder := newSlotTestRun(t, 1)
	if !r.bridge.Broadcaster.StartJob(slotTestConversationID, func() {}) {
		t.Fatal("StartJob refused a fresh conversation")
	}
	defer r.bridge.Broadcaster.EndJob(slotTestConversationID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, _, err := r.acquireSlotWithGate(ctx, cancel)
	if release != nil {
		t.Fatal("acquireSlot returned a release func alongside an error")
	}
	if err == nil || err.Error() != "该话题已有任务正在运行" {
		t.Fatalf("error = %v, want the busy-conversation notice", err)
	}
	// The ack must stay behind the busy check: a refused message never claimed
	// the thread, so it must not announce that it started.
	if got := recorder.All(); len(got) != 0 {
		t.Fatalf("thread updates = %q, want none for a refused run", got)
	}
	if n := r.bridge.Drainer.InFlight(); n != 0 {
		t.Fatalf("drainer in-flight = %d, want 0; the pool was never reached", n)
	}
	waitForPoolSlot(t, pool)
}

func TestAcquireSlotUndoesEarlierAcquisitionsWhenPoolIsCoolingDown(t *testing.T) {
	r, pool, recorder := newSlotTestRun(t, 1)
	pool.Cooldown(slotTestAccount, slotTestModel, time.Now().Add(30*time.Minute))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, _, err := r.acquireSlotWithGate(ctx, cancel)
	if release != nil {
		t.Fatal("acquireSlot returned a release func alongside an error")
	}
	if err == nil || !strings.HasPrefix(err.Error(), "账号限流中，请 ") {
		t.Fatalf("error = %v, want the cooldown notice", err)
	}
	if got := recorder.All(); len(got) != 1 || got[0] != AckText {
		t.Fatalf("thread updates = %q, want only the ack posted before the pool was entered", got)
	}
	if n := r.bridge.Drainer.InFlight(); n != 0 {
		t.Fatalf("drainer in-flight = %d, want the queued job rolled back", n)
	}
	if !r.bridge.Broadcaster.StartJob(slotTestConversationID, func() {}) {
		t.Fatal("a failed acquisition left the Broadcaster job registered")
	}
	r.bridge.Broadcaster.EndJob(slotTestConversationID)
}

// gatedQueueResponder holds the first queue-position update inside the platform
// call, the way a slow Slack or Feishu API does. Without it the relay goroutine
// finishes far too fast for a test to observe an update racing past the caller.
type gatedQueueResponder struct {
	*imbottest.ReplyRecorder
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gatedQueueResponder) Update(ctx context.Context, text string) error {
	if strings.Contains(text, "我当前在排队") {
		g.once.Do(func() {
			close(g.entered)
			<-g.release
		})
	}
	return g.ReplyRecorder.Update(ctx, text)
}

func TestAcquireSlotReportsQueuePositionsBeforeTheThinkingBanner(t *testing.T) {
	r, pool, recorder := newSlotTestRun(t, 1)
	gated := &gatedQueueResponder{
		ReplyRecorder: recorder,
		entered:       make(chan struct{}),
		release:       make(chan struct{}),
	}
	r.responder = gated
	holder := holdOnlySlot(t, pool)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		release func()
		err     error
	}
	done := make(chan result, 1)
	go func() {
		release, _, err := r.acquireSlotWithGate(ctx, cancel)
		done <- result{release: release, err: err}
	}()

	select {
	case <-gated.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("no queue position was posted while the account was busy")
	}
	// A queued IM turn must not inflate the admin view's running count.
	jobs := r.bridge.Drainer.Jobs()
	if len(jobs) != 1 || jobs[0].Status != service.JobStatusQueued {
		t.Fatalf("drainer jobs while queued = %+v, want a single %q job", jobs, service.JobStatusQueued)
	}

	// Grant the slot while the queue update is still wedged in the platform
	// call. acquireSlot must come back anyway — holding the slot until Slack
	// answers is what this whole hand-off exists to avoid.
	holder.Release()

	var got result
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("acquireSlot did not return until the in-flight queue update finished; the account slot was held for the IM round-trip")
	}
	if got.err != nil {
		t.Fatalf("acquireSlot: %v", got.err)
	}
	defer got.release()

	// Ordering is now the announcer's responsibility instead of the caller's:
	// let the held update land and join, and the banner must still come last.
	close(gated.release)
	r.announce.wait()

	texts := recorder.All()
	if texts[0] != AckText {
		t.Fatalf("first update = %q, want the ack", texts[0])
	}
	if last := texts[len(texts)-1]; last != thinkingStatusText {
		t.Fatalf("last update = %q, want the thinking banner; an in-flight queue position overwrote it", last)
	}
	for _, text := range texts[1 : len(texts)-1] {
		if !strings.Contains(text, "我当前在排队") {
			t.Fatalf("update between ack and thinking banner = %q, want only queue positions", text)
		}
	}
}

// wedgedProgressResponder never answers a progress write, the way a hung
// Slack/Feishu API does. respondBestEffort's 30s budget cannot rescue it: the
// platform call is what ignores the deadline.
type wedgedProgressResponder struct {
	*imbottest.ReplyRecorder
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *wedgedProgressResponder) Update(ctx context.Context, text string) error {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.ReplyRecorder.Update(ctx, text)
}

func TestAcquireSlotDoesNotHoldTheAccountSlotForTheThinkingBanner(t *testing.T) {
	r, pool, recorder := newSlotTestRun(t, 1)
	wedged := &wedgedProgressResponder{
		ReplyRecorder: recorder,
		entered:       make(chan struct{}),
		release:       make(chan struct{}),
	}
	r.responder = wedged

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		release func()
		err     error
	}
	done := make(chan result, 1)
	go func() {
		release, _, err := r.acquireSlotWithGate(ctx, cancel)
		done <- result{release: release, err: err}
	}()

	select {
	case <-wedged.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the thinking banner was never attempted")
	}

	// The slot is already granted at this point, so every second spent waiting
	// for the platform is a second the account cannot serve anyone else.
	var got result
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("acquireSlot blocked on the IM round-trip while holding an account slot; the agent cannot start")
	}
	if got.err != nil {
		t.Fatalf("acquireSlot: %v", got.err)
	}
	if jobs := r.bridge.Drainer.Jobs(); len(jobs) != 1 || jobs[0].Status != service.JobStatusRunning {
		t.Fatalf("drainer jobs = %+v, want a single %q job; the run is past acquisition", jobs, service.JobStatusRunning)
	}

	close(wedged.release)
	r.announce.wait()
	if last := recorder.All()[len(recorder.All())-1]; last != thinkingStatusText {
		t.Fatalf("last update = %q, want the thinking banner to still be delivered", last)
	}
	got.release()
	waitForPoolSlot(t, pool)
}

func TestStreamResponderWaitsForTheQueueAnnouncer(t *testing.T) {
	recorder := &imbottest.ReplyRecorder{}
	announced := make(chan struct{})
	r := &imRun{responder: recorder, imQueueState: imQueueState{announce: &imQueueAnnouncer{done: announced}}}

	written := make(chan error, 1)
	go func() { written <- r.streamResponder().Update(context.Background(), "💭 正在思考…") }()

	select {
	case <-written:
		t.Fatal("the agent stream wrote the progress message while the queue announcer still owned it")
	case <-time.After(50 * time.Millisecond):
	}

	close(announced)
	select {
	case err := <-written:
		if err != nil {
			t.Fatalf("progress update after the announcer finished: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("progress update never landed after the announcer finished")
	}
}

func TestAcquireSlotReleasesEverythingWhenTheQueueWaitIsCancelled(t *testing.T) {
	r, pool, recorder := newSlotTestRun(t, 1)
	holder := holdOnlySlot(t, pool)
	defer holder.Release()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		release func()
		err     error
	}
	done := make(chan result, 1)
	go func() {
		release, _, err := r.acquireSlotWithGate(ctx, cancel)
		done <- result{release: release, err: err}
	}()

	imbottest.WaitForReplyContaining(t, recorder, "我当前在排队")
	cancel()

	var got result
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("acquireSlot did not return after the wait was cancelled")
	}
	if got.release != nil {
		t.Fatal("acquireSlot returned a release func alongside an error")
	}
	// Cancelled, not timed out: telling the user to retry something they
	// deliberately stopped is worse than saying nothing.
	if got.err == nil || got.err.Error() != "排队期间已取消" {
		t.Fatalf("error = %v, want the cancellation notice", got.err)
	}
	for _, text := range recorder.All() {
		if text == thinkingStatusText {
			t.Fatal("a run that never got a slot posted the thinking banner")
		}
	}
	if n := r.bridge.Drainer.InFlight(); n != 0 {
		t.Fatalf("drainer in-flight = %d, want the queued job rolled back", n)
	}
	if !r.bridge.Broadcaster.StartJob(slotTestConversationID, func() {}) {
		t.Fatal("a cancelled wait left the Broadcaster job registered")
	}
	r.bridge.Broadcaster.EndJob(slotTestConversationID)
}

// recordingIMBackend captures what execute handed the backend and replays a
// scripted event stream.
type recordingIMBackend struct {
	events        []agent.StreamEvent
	sessionExists bool
	runErr        error
	blockUntilCtx bool

	prompt  string
	workDir string
	opts    agent.RunRequest
}

func (*recordingIMBackend) Name() string                     { return "recording-im" }
func (*recordingIMBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b *recordingIMBackend) RunWithSession(ctx context.Context, prompt, workDir string, opts agent.RunRequest, ch chan<- agent.StreamEvent) error {
	b.prompt, b.workDir, b.opts = prompt, workDir, opts
	for _, evt := range b.events {
		ch <- evt
	}
	if b.blockUntilCtx {
		<-ctx.Done()
		close(ch)
		return ctx.Err()
	}
	close(ch)
	return b.runErr
}
func (*recordingIMBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (b *recordingIMBackend) SessionExists(string, string, string) bool { return b.sessionExists }
func (*recordingIMBackend) SessionLogPath(string, string, string) string {
	return ""
}

func newExecuteTestRun(t *testing.T, backend agent.Backend) *imRun {
	t.Helper()
	r, _, _ := newSlotTestRun(t, 1)
	r.bridge.Backends = service.NewBackendRegistry(map[string]agent.Backend{config.CLITypeClaude: backend}, nil)
	r.rule = imbot.ChannelRule{ExtraPrompt: "  回复请附上工单号  "}
	r.thread = store.BotThread{
		Platform: threadPlatform(r.msg), ChannelID: r.msg.ChannelID, ThreadID: r.msg.ThreadID,
		AgentID: r.agentUser.ID, ConversationID: slotTestConversationID,
		Provider: config.CLITypeClaude, Model: slotTestModel,
	}
	return r
}

func TestExecuteBuildsTheRequestAndPersistsWhatTheTurnLearned(t *testing.T) {
	backend := &recordingIMBackend{events: []agent.StreamEvent{
		{Kind: agent.KindSystemInit, Content: `{"session_id":"cli-session-7"}`},
		{Kind: agent.KindResult, Content: "已完成部署"},
	}}
	r := newExecuteTestRun(t, backend)
	// Path addresses the file for the browser; LocalPath is what the agent can
	// actually open. Only the latter belongs in the prompt.
	r.attachments = []IMImageMetadata{{
		Name:      "shot.png",
		Path:      ".daymug/agents/agent1/slack/shot.png",
		LocalPath: "/home/alice/.daymug/agents/agent1/slack/shot.png",
	}}

	content, err := r.execute(context.Background(), context.Background())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if content != "已完成部署" {
		t.Fatalf("content = %q, want the agent reply", content)
	}

	if backend.opts.Model != slotTestModel {
		t.Fatalf("request model = %q, want %q", backend.opts.Model, slotTestModel)
	}
	if backend.opts.SessionID == "" {
		t.Fatal("no session id was minted for a claude turn; the thread cannot resume")
	}
	if backend.opts.IsResume {
		t.Fatal("IsResume set for a session log that does not exist yet")
	}
	if backend.workDir != r.agentUser.WorkDir {
		t.Fatalf("work dir = %q, want the agent's %q", backend.workDir, r.agentUser.WorkDir)
	}
	if !strings.Contains(backend.opts.SystemPrompt, "回复请附上工单号") {
		t.Fatalf("system prompt = %q, want the channel rule's extra prompt appended", backend.opts.SystemPrompt)
	}
	if !strings.Contains(backend.prompt, "Attached files:\n/home/alice/.daymug/agents/agent1/slack/shot.png") {
		t.Fatalf("prompt = %q, want the attachment listed at the path the agent can open", backend.prompt)
	}

	fake := r.bridge.Store.(*storetest.Fake)
	thread, err := fake.GetBotThread(context.Background(), threadPlatform(r.msg), r.msg.ChannelID, r.msg.ThreadID)
	if err != nil {
		t.Fatalf("read persisted thread: %v", err)
	}
	if thread.SessionID != "cli-session-7" {
		t.Fatalf("persisted session id = %q, want the id the backend reported", thread.SessionID)
	}
	if thread.LastMessageID != r.msg.MessageID {
		t.Fatalf("persisted cursor = %q, want it advanced to %q", thread.LastMessageID, r.msg.MessageID)
	}
}

func TestExecuteResumesAnExistingThreadSession(t *testing.T) {
	backend := &recordingIMBackend{
		sessionExists: true,
		events:        []agent.StreamEvent{{Kind: agent.KindResult, Content: "继续"}},
	}
	r := newExecuteTestRun(t, backend)
	r.thread.SessionID = "sticky-session"

	if _, err := r.execute(context.Background(), context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if backend.opts.SessionID != "sticky-session" || !backend.opts.IsResume {
		t.Fatalf("request session = %q resume = %v, want the sticky session resumed", backend.opts.SessionID, backend.opts.IsResume)
	}
}

// ctxSensitiveStore mimics database/sql, which refuses any statement carrying
// an already-cancelled context.
type ctxSensitiveStore struct {
	*storetest.Fake
}

func (s *ctxSensitiveStore) SetSessionID(ctx context.Context, conversationID, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Fake.SetSessionID(ctx, conversationID, sessionID)
}

func (s *ctxSensitiveStore) UpsertBotThread(ctx context.Context, thread store.BotThread) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Fake.UpsertBotThread(ctx, thread)
}

func TestExecutePersistsThreadBindingEvenWhenTheTurnIsInterrupted(t *testing.T) {
	backend := &recordingIMBackend{blockUntilCtx: true}
	r := newExecuteTestRun(t, backend)
	fake := storetest.New()
	r.bridge.Store = &ctxSensitiveStore{Fake: fake}

	// Preemption by a newer message in the same thread cancels both the agent
	// context and the surrounding run context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := r.execute(ctx, ctx); err == nil {
		t.Fatal("execute returned no error for a cancelled turn")
	}

	thread, err := fake.GetBotThread(context.Background(), threadPlatform(r.msg), r.msg.ChannelID, r.msg.ThreadID)
	if err != nil {
		t.Fatalf("interrupted turn wrote no thread binding: %v", err)
	}
	if thread.LastMessageID != r.msg.MessageID {
		t.Fatalf("persisted cursor = %q, want it advanced to %q even on an interrupted turn", thread.LastMessageID, r.msg.MessageID)
	}
	if thread.SessionID == "" {
		t.Fatal("the minted session id was dropped; the next turn would start a fresh CLI session")
	}
}

func TestExecuteReportsAgentFailures(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		backend := &recordingIMBackend{blockUntilCtx: true}
		r := newExecuteTestRun(t, backend)
		agentCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		_, err := r.execute(agentCtx, context.Background())
		if err == nil || err.Error() != "agent 运行超时，已中止" {
			t.Fatalf("error = %v, want the run-timeout notice", err)
		}
	})

	t.Run("backend failure", func(t *testing.T) {
		backend := &recordingIMBackend{runErr: errors.New("boom")}
		r := newExecuteTestRun(t, backend)

		_, err := r.execute(context.Background(), context.Background())
		if err == nil || !strings.HasPrefix(err.Error(), "agent 运行失败: ") {
			t.Fatalf("error = %v, want the run-failure notice", err)
		}
	})
}

func TestAgentRunErrorMapsDeadlinesToTheTimeoutNotice(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	tests := []struct {
		name     string
		agentCtx context.Context
		runErr   error
		want     string
	}{
		// The streamer reports a killed child as a plain command failure, so
		// only the context still knows the IM run limit is what fired.
		{name: "deadline on the turn context", agentCtx: expired, runErr: errors.New("signal: killed"), want: "agent 运行超时，已中止"},
		{name: "deadline on the error", agentCtx: context.Background(), runErr: context.DeadlineExceeded, want: "agent 运行超时，已中止"},
		{name: "plain failure", agentCtx: context.Background(), runErr: errors.New("boom"), want: "agent 运行失败: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentRunError(tt.agentCtx, tt.runErr); got.Error() != tt.want {
				t.Fatalf("agentRunError = %q, want %q", got, tt.want)
			}
		})
	}
}

// A queued turn on a platform with no edit primitive used to be indistinguishable
// from a bot that ignored you: the position went out through Update, which
// writes nowhere there, and nothing later supersedes a position anyway.
func TestAcquireSlotReportsQueuePositionsWithoutEditPrimitive(t *testing.T) {
	r, pool, _ := newSlotTestRun(t, 1)
	rec := &noEditResponder{}
	r.responder = rec
	holder := holdOnlySlot(t, pool)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan func(), 1)
	go func() {
		release, _, err := r.acquireSlotWithGate(ctx, cancel)
		if err != nil {
			t.Errorf("acquireSlot: %v", err)
		}
		done <- release
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		notices, _ := rec.snapshot()
		if len(notices) > 0 {
			if !strings.Contains(notices[0], "我当前在排队") {
				t.Fatalf("first notice = %q, want the queue position", notices[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no queue position reached a reader who cannot be sent edits")
		}
		time.Sleep(5 * time.Millisecond)
	}

	holder.Release()
	select {
	case release := <-done:
		if release != nil {
			release()
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquireSlot did not return after the slot was granted")
	}
	r.announce.wait()
}

// A draining server refuses after the ack (the room was already claimed) and
// hands the room back; the pool is never entered.
func TestAcquireSlotRefusesWhileDraining(t *testing.T) {
	r, pool, recorder := newSlotTestRun(t, 1)
	r.bridge.Drainer.StartDrain(service.DrainReasonUpgrade)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, _, err := r.acquireSlotWithGate(ctx, cancel)
	if release != nil {
		t.Fatal("acquireSlot returned a release func alongside an error")
	}
	if err == nil || err.Error() != "服务正在重启，请稍后重试" {
		t.Fatalf("error = %v, want the restart notice", err)
	}
	if got := recorder.All(); len(got) != 1 || got[0] != AckText {
		t.Fatalf("thread updates = %q, want only the ack", got)
	}
	if !r.bridge.Broadcaster.StartJob(slotTestConversationID, func() {}) {
		t.Fatal("a refused acquisition left the Broadcaster job registered")
	}
	r.bridge.Broadcaster.EndJob(slotTestConversationID)
	if _, queued, _ := pool.Stats(slotTestAccount); queued != 0 {
		t.Fatalf("pool queued = %d, want the pool never entered", queued)
	}
}

// The 30-minute queue budget is reported as a timeout, distinct from a cancel.
func TestAcquireSlotReportsQueueTimeout(t *testing.T) {
	r, pool, _ := newSlotTestRun(t, 1)
	holder := holdOnlySlot(t, pool)
	defer holder.Release()
	prev := imQueueWait
	imQueueWait = 50 * time.Millisecond
	defer func() { imQueueWait = prev }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, _, err := r.acquireSlotWithGate(ctx, cancel)
	if release != nil {
		t.Fatal("acquireSlot returned a release func alongside an error")
	}
	if err == nil || err.Error() != "等待账号空闲超时，请稍后重试" {
		t.Fatalf("error = %v, want the queue-timeout notice", err)
	}
	if n := r.bridge.Drainer.InFlight(); n != 0 {
		t.Fatalf("drainer in-flight = %d, want 0", n)
	}
	if _, queued, _ := pool.Stats(slotTestAccount); queued != 0 {
		t.Fatalf("pool queued = %d, want the abandoned ticket gone", queued)
	}
}

// selfAssigningIMBackend picks its own session ids, the way codex does.
type selfAssigningIMBackend struct{ recordingIMBackend }

func (*selfAssigningIMBackend) Capabilities() agent.Capabilities {
	return agent.Capabilities{AssignsSessionID: true}
}

// A new thread on a backend that assigns its own session ids starts with no
// minted id; the one it reports is what the thread keeps. The decision is the
// backend's capability, not a provider-name check.
func TestExecuteDoesNotMintForSelfAssigningBackend(t *testing.T) {
	backend := &selfAssigningIMBackend{recordingIMBackend{events: []agent.StreamEvent{
		{Kind: agent.KindSystemInit, Content: `{"session_id":"thread-9"}`},
		{Kind: agent.KindResult, Content: "ok"},
	}}}
	r := newExecuteTestRun(t, backend)

	if _, err := r.execute(context.Background(), context.Background()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if backend.opts.SessionID != "" || backend.opts.IsResume {
		t.Fatalf("opts session = %q resume=%v, want a new session with no minted id", backend.opts.SessionID, backend.opts.IsResume)
	}
	thread, err := r.bridge.Store.(*storetest.Fake).GetBotThread(context.Background(), threadPlatform(r.msg), r.msg.ChannelID, r.msg.ThreadID)
	if err != nil {
		t.Fatalf("read persisted thread: %v", err)
	}
	if thread.SessionID != "thread-9" {
		t.Fatalf("persisted session id = %q, want the backend's own", thread.SessionID)
	}
}
