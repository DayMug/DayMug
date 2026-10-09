package imbridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/agent/agenttest"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/imbot/imbottest"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// artifactIMBackend reproduces how codex closes an IM turn: several KindResult
// chunks, the last of which carries the publish marker on its own line.
type artifactIMBackend struct{ results []string }

func (artifactIMBackend) Name() string                     { return "artifact-im" }
func (artifactIMBackend) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (b artifactIMBackend) RunWithSession(_ context.Context, _, _ string, _ agent.RunRequest, ch chan<- agent.StreamEvent) error {
	for _, r := range b.results {
		ch <- agent.StreamEvent{Kind: agent.KindResult, Content: r}
	}
	close(ch)
	return nil
}
func (artifactIMBackend) RunOneshot(context.Context, string, string, agent.RunRequest) (string, error) {
	return "", nil
}
func (artifactIMBackend) SessionExists(string, string, string) bool    { return false }
func (artifactIMBackend) SessionLogPath(string, string, string) string { return "" }

// A one-pixel PNG, so the MIME sniff sees a real image rather than octet-stream.
var onePixelPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0, 0, 0, 0x0d, 'I', 'H', 'D', 'R',
	0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89,
	0, 0, 0, 0x0a, 'I', 'D', 'A', 'T', 0x78, 0x9c, 0x63, 0, 1, 0, 0, 5, 0, 1,
	0x0d, 0x0a, 0x2d, 0xb4, 0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

// An IM turn that publishes a file must hand that file back to the caller, which
// is the only thing that makes it reach Slack/飞书 as an upload.
func TestIMRunReturnsPublishedArtifacts(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "shot.png"), onePixelPNG, 0o600); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	bridge := &IMBridge{Runtime: &service.Runtime{Store: storetest.New()}}
	_, artifacts, _, err := bridge.runAndStream(
		context.Background(),
		artifactIMBackend{results: []string{"验收完成\n[DAYMUG_ARTIFACT ./shot.png]"}},
		"prompt", workDir, "agent-1", "acc1", agent.RunRequest{},
		"conversation", imbot.Message{Platform: "slack", ChannelID: "channel", ThreadID: "thread"},
		&imbottest.ReplyRecorder{},
	)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(artifacts.Published) != 1 {
		t.Fatalf("artifacts = %d, want 1 — a published file the IM turn never returns is never uploaded", len(artifacts.Published))
	}
	if artifacts.Published[0].Name != "shot.png" {
		t.Fatalf("artifact name = %q, want shot.png", artifacts.Published[0].Name)
	}
	_ = service.Artifact{}
}

// artifactResponderSpy is a Responder that can also take uploads, matching the
// Slack / 飞书 / Telegram connectors. It records what the bridge hands it.
type artifactResponderSpy struct {
	imbottest.ReplyRecorder
	// failOn names the single attachment whose upload is rejected, modelling a
	// platform that refuses one file out of a batch.
	failOn   string
	mu       sync.Mutex
	uploaded []string
	// answer models what an edit-capable connector leaves on screen: Complete
	// writes it, Update rewrites it in place, Post appends beside it.
	answer string
	posts  []string
}

func (r *artifactResponderSpy) Complete(ctx context.Context, text string) error {
	r.mu.Lock()
	r.answer = text
	r.mu.Unlock()
	return r.ReplyRecorder.Complete(ctx, text)
}

func (r *artifactResponderSpy) Update(ctx context.Context, text string) error {
	r.mu.Lock()
	r.answer = text
	r.mu.Unlock()
	return r.ReplyRecorder.Update(ctx, text)
}

func (r *artifactResponderSpy) Post(ctx context.Context, text string) error {
	r.mu.Lock()
	r.posts = append(r.posts, text)
	r.mu.Unlock()
	return r.ReplyRecorder.Post(ctx, text)
}

func (r *artifactResponderSpy) screen() (answer string, posts []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.answer, append([]string(nil), r.posts...)
}

func (r *artifactResponderSpy) PostAttachments(_ context.Context, attachments []imbot.OutboundAttachment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range attachments {
		if a.Name == r.failOn {
			return errors.New("slack get upload URL: not_in_channel")
		}
		r.uploaded = append(r.uploaded, a.Name)
	}
	return nil
}

func (r *artifactResponderSpy) uploads() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.uploaded...)
}

// The whole point of publishing a file from an IM thread is that the thread
// receives it. This drives the real HandleMessage path a Slack message takes.
func TestIMTurnUploadsPublishedArtifactsToTheThread(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "shot.png"), onePixelPNG, 0o600); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	useAccountModels(t, map[string][]string{"acc1": {agenttest.Model}})
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "acc1", Type: config.CLITypeClaude, MaxConcurrent: 1,
	}}}
	ms := storetest.New()
	ms.Users = []store.User{{
		ID: "owner1", Username: "owner", Name: "Owner", WorkDir: workDir,
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}}
	bridge := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, artifactIMBackend{results: []string{"验收完成\n[DAYMUG_ARTIFACT ./shot.png]"}})}, Bots: ms,
	}
	addInterruptTestAgent(ms, "slack", "agent-1", "bot-1", workDir)

	spy := &artifactResponderSpy{}
	bridge.HandleMessage(context.Background(),
		interruptTestMessage("slack", "agent-1", "bot-1", "m1"), spy)

	if got := spy.uploads(); len(got) != 1 || got[0] != "shot.png" {
		t.Fatalf("uploads = %v, want [shot.png] — the thread never received the published file", got)
	}
}

// codex closes each step with its own result frame, so a publish marker written
// at the end of one step sits directly in front of the next step's first line.
// The marker must survive that join — it is the only thing that turns a
// generated file into a thread upload.
func TestIMRunKeepsArtifactsAcrossMultipleResultChunks(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "shot.png"), onePixelPNG, 0o600); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	bridge := &IMBridge{Runtime: &service.Runtime{Store: storetest.New()}}
	content, artifacts, _, err := bridge.runAndStream(
		context.Background(),
		artifactIMBackend{results: []string{
			"验收完成\n[DAYMUG_ARTIFACT ./shot.png]",
			"随后继续部署前端。",
		}},
		"prompt", workDir, "agent-1", "acc1", agent.RunRequest{},
		"conversation", imbot.Message{Platform: "slack", ChannelID: "channel", ThreadID: "thread"},
		&imbottest.ReplyRecorder{},
	)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(artifacts.Published) != 1 {
		t.Fatalf("artifacts = %d, want 1 — the marker was swallowed by the chunk join", len(artifacts.Published))
	}
	if strings.Contains(content, "DAYMUG_ARTIFACT") {
		t.Fatalf("reply still shows the raw marker: %q", content)
	}
}

// A file the platform refuses must not take the rest of the batch with it, and
// the reader has to be told — the answer above talks about all of them.
func TestIMTurnReportsFailedArtifactUploadsAndKeepsGoing(t *testing.T) {
	workDir := t.TempDir()
	for _, name := range []string{"report.json", "shot.png"} {
		if err := os.WriteFile(filepath.Join(workDir, name), onePixelPNG, 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	useAccountModels(t, map[string][]string{"acc1": {agenttest.Model}})
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "acc1", Type: config.CLITypeClaude, MaxConcurrent: 1,
	}}}
	ms := storetest.New()
	ms.Users = []store.User{{
		ID: "owner1", Username: "owner", Name: "Owner", WorkDir: workDir,
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}}
	bridge := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, artifactIMBackend{results: []string{
			"验收完成\n[DAYMUG_ARTIFACT ./report.json]\n[DAYMUG_ARTIFACT ./shot.png]",
		}})}, Bots: ms,
	}
	addInterruptTestAgent(ms, "slack", "agent-1", "bot-1", workDir)

	// report.json leads the batch, which is the shape that used to swallow
	// every file behind it.
	spy := &artifactResponderSpy{failOn: "report.json"}
	bridge.HandleMessage(context.Background(),
		interruptTestMessage("slack", "agent-1", "bot-1", "m1"), spy)

	if got := spy.uploads(); len(got) != 1 || got[0] != "shot.png" {
		t.Fatalf("uploads = %v, want [shot.png] — one rejected file blocked the rest", got)
	}
	answer, posts := spy.screen()
	var notice string
	for _, text := range posts {
		if strings.Contains(text, "未能送达") {
			notice = text
		}
	}
	if notice == "" {
		t.Fatalf("no upload-failure notice reached the thread; posts = %v", posts)
	}
	if !strings.Contains(notice, "report.json") || !strings.Contains(notice, "not_in_channel") {
		t.Fatalf("notice names neither the file nor the reason: %q", notice)
	}
	// The notice annotates the answer, so it must arrive beside it. Delivering
	// it through Update would edit the message the answer is sitting in.
	if !strings.Contains(answer, "验收完成") {
		t.Fatalf("answer was overwritten by the failure notice: %q", answer)
	}
}

// A marker that never resolves to a file produces no upload to fail, so it used
// to leave no trace anywhere the reader or the agent could see it — the answer
// went out claiming an attachment, the human said "I don't see it", and the
// agent re-sent the identical broken marker. It has to be reported like a
// failed upload, because to the reader it is one.
func TestIMTurnReportsMarkersThatResolvedToNoFile(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "shot.png"), onePixelPNG, 0o600); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	useAccountModels(t, map[string][]string{"acc1": {agenttest.Model}})
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "acc1", Type: config.CLITypeClaude, MaxConcurrent: 1,
	}}}
	ms := storetest.New()
	ms.Users = []store.User{{
		ID: "owner1", Username: "owner", Name: "Owner", WorkDir: workDir,
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}}
	bridge := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, artifactIMBackend{results: []string{
			"验收完成\n[DAYMUG_ARTIFACT ./shot.png]\n[DAYMUG_ARTIFACT ./candidate.pdf]",
		}})}, Bots: ms,
	}
	addInterruptTestAgent(ms, "slack", "agent-1", "bot-1", workDir)

	spy := &artifactResponderSpy{}
	bridge.HandleMessage(context.Background(),
		interruptTestMessage("slack", "agent-1", "bot-1", "m1"), spy)

	// The valid file still goes up: one bad marker must not cost the reader the
	// files that were real.
	if got := spy.uploads(); len(got) != 1 || got[0] != "shot.png" {
		t.Fatalf("uploads = %v, want [shot.png]", got)
	}
	answer, posts := spy.screen()
	var notice string
	for _, text := range posts {
		if strings.Contains(text, "未能送达") {
			notice = text
		}
	}
	if notice == "" {
		t.Fatalf("a marker that resolved to nothing was dropped silently; posts = %v", posts)
	}
	if !strings.Contains(notice, "./candidate.pdf") || !strings.Contains(notice, "file not found") {
		t.Fatalf("notice names neither the marker nor the reason: %q", notice)
	}
	if strings.Contains(notice, workDir) {
		t.Fatalf("notice leaks the host path: %q", notice)
	}
	if !strings.Contains(answer, "验收完成") {
		t.Fatalf("answer was overwritten by the notice: %q", answer)
	}
}
