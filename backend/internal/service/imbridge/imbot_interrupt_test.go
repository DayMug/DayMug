package imbridge

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/agent/agenttest"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"

	"github.com/DayMug/DayMug/backend/internal/imbot/imbottest"
)

type interruptionResponder struct {
	mu       sync.Mutex
	progress string
	posts    []string
	complete []string
}

func (r *interruptionResponder) Start(_ context.Context, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = text
	return nil
}

func (r *interruptionResponder) Update(ctx context.Context, text string) error {
	return r.Start(ctx, text)
}

func (r *interruptionResponder) Complete(ctx context.Context, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = text
	r.complete = append(r.complete, text)
	return nil
}

func (r *interruptionResponder) Post(_ context.Context, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.posts = append(r.posts, text)
	return nil
}

func (r *interruptionResponder) snapshot() (string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.progress, append([]string(nil), r.posts...)
}

func (r *interruptionResponder) completions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.complete...)
}

// lateResultOnCancelBackend models a backend transport that already has a
// final event buffered when cancellation lands. The event is valid input to
// the shared streamer, but it must not become an outbound IM reply.
type lateResultOnCancelBackend struct {
	started chan<- struct{}
}

func (lateResultOnCancelBackend) Name() string { return "late-result-on-cancel" }
func (lateResultOnCancelBackend) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}
func (b lateResultOnCancelBackend) RunWithSession(ctx context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	b.started <- struct{}{}
	<-ctx.Done()
	ch <- agent.StreamEvent{Kind: agent.KindResult, Content: "must not be published"}
	close(ch)
	return nil
}
func (lateResultOnCancelBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (lateResultOnCancelBackend) SessionExists(string, string, string) bool    { return false }
func (lateResultOnCancelBackend) SessionLogPath(string, string, string) string { return "" }

func newInterruptTestBridge(t *testing.T, maxConcurrent int, started chan<- struct{}, release <-chan struct{}) (*IMBridge, *storetest.Fake) {
	t.Helper()
	useAccountModels(t, map[string][]string{"acc1": {agenttest.Model}})
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "acc1", Type: config.CLITypeClaude, MaxConcurrent: maxConcurrent,
	}}}
	ms := storetest.New()
	ms.Users = []store.User{{
		ID: "owner1", Username: "owner", Name: "Owner",
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}}
	return &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, blockingIMBackend{started: started, release: release})}, Bots: ms,
	}, ms
}

func addInterruptTestAgent(ms *storetest.Fake, platform, agentID, botID, workDir string) {
	ms.Users = append(ms.Users, store.User{
		ID: agentID, Name: agentID, OwnerID: "owner1", WorkDir: workDir,
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	})
	ms.Bots = append(ms.Bots, store.Bot{
		ID: botID, AgentID: agentID, Platform: platform, Enabled: true,
		Channels: `[{"channel":"*","auto_reply":true}]`,
	})
}

func interruptTestMessage(platform, agentID, botID, messageID string) imbot.Message {
	return imbot.Message{
		Platform: platform, AgentID: agentID, BotID: botID,
		ChannelID: "C1", ThreadID: "thread1", MessageID: messageID,
		Text: messageID, Mentioned: true,
	}
}

func waitForIMRunStart(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for IM run to start")
	}
}

func waitForIMHandle(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for IM handler to finish")
	}
}

func TestIMMessageSupersedesActiveTurnForSameBotThread(t *testing.T) {
	for _, platform := range []string{imbot.PlatformSlack, imbot.PlatformFeishu} {
		t.Run(platform, func(t *testing.T) {
			started := make(chan struct{}, 2)
			release := make(chan struct{})
			bridge, ms := newInterruptTestBridge(t, 1, started, release)
			addInterruptTestAgent(ms, platform, "agent1", "bot1", t.TempDir())

			firstReplies := &imbottest.ReplyRecorder{}
			firstDone := make(chan struct{})
			go func() {
				defer close(firstDone)
				bridge.HandleMessage(context.Background(), interruptTestMessage(platform, "agent1", "bot1", "first"), firstReplies)
			}()
			waitForIMRunStart(t, started)

			secondReplies := &imbottest.ReplyRecorder{}
			secondDone := make(chan struct{})
			go func() {
				defer close(secondDone)
				bridge.HandleMessage(context.Background(), interruptTestMessage(platform, "agent1", "bot1", "second"), secondReplies)
			}()

			imbottest.WaitForReplyContaining(t, firstReplies, "已被同一话题中的新消息取消")
			waitForIMRunStart(t, started)
			close(release)
			waitForIMHandle(t, firstDone)
			waitForIMHandle(t, secondDone)
			imbottest.WaitForReplyContaining(t, secondReplies, "done")
		})
	}
}

func TestWebCancelDoesNotPublishBufferedIMResult(t *testing.T) {
	started := make(chan struct{}, 1)
	bridge, ms := newInterruptTestBridge(t, 1, started, make(chan struct{}))
	bridge.Backends = service.NewBackendRegistry(nil, lateResultOnCancelBackend{started: started})
	addInterruptTestAgent(ms, imbot.PlatformSlack, "agent1", "bot1", t.TempDir())
	msg := interruptTestMessage(imbot.PlatformSlack, "agent1", "bot1", "queued-message")
	responder := &interruptionResponder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		bridge.HandleMessage(context.Background(), msg, responder)
	}()
	waitForIMRunStart(t, started)

	thread, err := ms.GetBotThread(context.Background(), threadPlatform(msg), msg.ChannelID, msg.ThreadID)
	if err != nil {
		t.Fatalf("load bound thread: %v", err)
	}
	if !bridge.Broadcaster.CancelJob(thread.ConversationID) {
		t.Fatal("web cancel did not find the active IM job")
	}
	waitForIMHandle(t, done)

	if got := responder.completions(); len(got) != 0 {
		t.Fatalf("completed IM replies = %q, want none after web cancel", got)
	}
}

func TestIMMessageSupersedePreservesExistingProgress(t *testing.T) {
	for _, platform := range []string{imbot.PlatformSlack, imbot.PlatformFeishu} {
		t.Run(platform, func(t *testing.T) {
			started := make(chan struct{}, 2)
			release := make(chan struct{})
			bridge, ms := newInterruptTestBridge(t, 1, started, release)
			addInterruptTestAgent(ms, platform, "agent1", "bot1", t.TempDir())

			firstReplies := &interruptionResponder{}
			firstDone := make(chan struct{})
			go func() {
				defer close(firstDone)
				bridge.HandleMessage(context.Background(), interruptTestMessage(platform, "agent1", "bot1", "first"), firstReplies)
			}()
			waitForIMRunStart(t, started)

			secondDone := make(chan struct{})
			go func() {
				defer close(secondDone)
				bridge.HandleMessage(context.Background(), interruptTestMessage(platform, "agent1", "bot1", "second"), &imbottest.ReplyRecorder{})
			}()

			waitForIMRunStart(t, started)
			close(release)
			waitForIMHandle(t, firstDone)
			waitForIMHandle(t, secondDone)

			progress, posts := firstReplies.snapshot()
			if strings.Contains(progress, supersededText) {
				t.Fatalf("interruption replaced existing progress: %q", progress)
			}
			if len(posts) != 1 || posts[0] != supersededText {
				t.Fatalf("interruption posts = %v, want one standalone %q notice", posts, supersededText)
			}
		})
	}
}

func TestIMMessageDoesNotCancelSiblingBotInSamePlatformThread(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	bridge, ms := newInterruptTestBridge(t, 2, started, release)
	addInterruptTestAgent(ms, imbot.PlatformSlack, "agentA", "botA", t.TempDir())
	addInterruptTestAgent(ms, imbot.PlatformSlack, "agentB", "botB", t.TempDir())

	firstReplies := &imbottest.ReplyRecorder{}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		bridge.HandleMessage(context.Background(), interruptTestMessage(imbot.PlatformSlack, "agentA", "botA", "for-a"), firstReplies)
	}()
	waitForIMRunStart(t, started)

	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		bridge.HandleMessage(context.Background(), interruptTestMessage(imbot.PlatformSlack, "agentB", "botB", "for-b"), &imbottest.ReplyRecorder{})
	}()
	waitForIMRunStart(t, started)

	select {
	case <-firstDone:
		t.Fatal("bot A was interrupted by a message addressed to bot B")
	default:
	}
	for _, reply := range firstReplies.All() {
		if strings.Contains(reply, "取消") {
			t.Fatalf("bot A received a cancellation update: %q", reply)
		}
	}

	close(release)
	waitForIMHandle(t, firstDone)
	waitForIMHandle(t, secondDone)
}
