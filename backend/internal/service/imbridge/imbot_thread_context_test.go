package imbridge

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent/agenttest"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/imbot/imbottest"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

func TestBoundThreadContextKeepsNewestWithinBudget(t *testing.T) {
	big := strings.Repeat("x", imThreadContextByteLimit/2-1) // cost is len+1, so exactly two fit

	tests := []struct {
		name        string
		rendered    []string
		wantKept    []string
		wantDropped int
	}{
		{
			name:     "everything fits",
			rendered: []string{"a", "b", "c"},
			wantKept: []string{"a", "b", "c"},
		},
		{
			name:        "message count is capped, newest survive",
			rendered:    numberedMessages(imThreadContextMessageLimit + 5),
			wantKept:    numberedMessages(imThreadContextMessageLimit + 5)[5:],
			wantDropped: 5,
		},
		{
			name:        "byte budget drops the oldest",
			rendered:    []string{big, big, big},
			wantKept:    []string{big, big},
			wantDropped: 1,
		},
		{
			name:     "the newest message is kept even when it alone exceeds the budget",
			rendered: []string{"older", strings.Repeat("y", imThreadContextByteLimit*2)},
			// Dropping the message right before the current turn would break the
			// short follow-ups this context exists to resolve.
			wantKept:    []string{strings.Repeat("y", imThreadContextByteLimit*2)},
			wantDropped: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kept, dropped := boundThreadContext(tc.rendered)
			if dropped != tc.wantDropped {
				t.Fatalf("dropped = %d, want %d", dropped, tc.wantDropped)
			}
			if len(kept) != len(tc.wantKept) {
				t.Fatalf("kept %d messages, want %d", len(kept), len(tc.wantKept))
			}
			for i := range kept {
				if kept[i] != tc.wantKept[i] {
					t.Fatalf("kept[%d] = %.20q…, want %.20q…", i, kept[i], tc.wantKept[i])
				}
			}
		})
	}
}

func TestBuildPromptWithThreadContextElidesOldestAndTruncatesLongMessages(t *testing.T) {
	b := &IMBridge{Runtime: &service.Runtime{}}
	history := make([]imbot.Message, imThreadContextMessageLimit+3)
	for i := range history {
		history[i] = imbot.Message{SenderName: "Ada", Text: fmt.Sprintf("progress %d", i)}
	}
	// A co-bot's full progress narration is the shape that made this expensive:
	// it is long, it is already summarised by the handoff instruction, and it is
	// re-injected on every handoff.
	history[len(history)-1].Text = strings.Repeat("z", imThreadContextPerMessageBytes*2)

	prompt := b.buildPromptWithThreadContext(imbot.Message{SenderName: "Guard", Text: "review it"}, history)

	if !strings.Contains(prompt, fmt.Sprintf(imThreadContextElidedTextf, 3)) {
		t.Fatalf("prompt does not report the 3 elided messages: %.200q", prompt)
	}
	if strings.Contains(prompt, "progress 0\n") || strings.Contains(prompt, "progress 2\n") {
		t.Fatalf("prompt still carries elided messages: %.200q", prompt)
	}
	if !strings.Contains(prompt, "progress 3\n") {
		t.Fatalf("prompt dropped a message that fits the window: %.200q", prompt)
	}
	if !strings.Contains(prompt, relayQuoteTruncatedSuffix) {
		t.Fatalf("oversized message was not truncated: %.200q", prompt)
	}
	if !strings.HasSuffix(prompt, "Current message:\n[Guard]: review it") {
		t.Fatalf("prompt does not end with the current message: %.200q", prompt)
	}
	if len(prompt) > imThreadContextByteLimit+imThreadContextPerMessageBytes+512 {
		t.Fatalf("prompt is %d bytes, well past the context budget", len(prompt))
	}
}

func TestBuildPromptWithThreadContextLeavesShortThreadsIntact(t *testing.T) {
	b := &IMBridge{Runtime: &service.Runtime{}}
	history := []imbot.Message{
		{SenderName: "Alice", Text: "root question"},
		{SenderName: "Helper", Text: "earlier answer"},
	}

	prompt := b.buildPromptWithThreadContext(imbot.Message{SenderName: "Carol", Text: "current question"}, history)

	want := "Earlier messages in this Slack thread, oldest first:\n" +
		"[Alice]: root question\n" +
		"[Helper]: earlier answer\n\n" +
		"Current message:\n[Carol]: current question"
	if prompt != want {
		t.Fatalf("prompt = %q, want %q", prompt, want)
	}
}

func TestBuildPromptWithThreadContextOmitsOtherAgentsOnHandoffTurns(t *testing.T) {
	b := &IMBridge{Runtime: &service.Runtime{}}
	history := []imbot.Message{
		{SenderName: "Ada", Text: "long progress narration one", FromBot: true},
		{SenderName: "韩音", Text: "灰度还是不行"},
		{SenderName: "Ada", Text: "long progress narration two", FromBot: true},
	}

	// A turn another agent handed over: the instruction is the request, so the
	// source agent's own prose would be the same content a second time.
	relayed := b.buildPromptWithThreadContext(
		imbot.Message{SenderName: "Ada", Text: "请审核 MR 3340", FromBot: true}, history)

	if strings.Contains(relayed, "long progress narration") {
		t.Fatalf("handoff prompt repeated the other agent's prose: %q", relayed)
	}
	if !strings.Contains(relayed, "[韩音]: 灰度还是不行") {
		t.Fatalf("handoff prompt dropped a human message: %q", relayed)
	}
	if !strings.Contains(relayed, fmt.Sprintf(imThreadContextRelayedTextf, 2)) {
		t.Fatalf("handoff prompt does not account for the 2 omitted agent messages: %q", relayed)
	}
	if !strings.HasSuffix(relayed, "Current message:\n[Ada]: 请审核 MR 3340") {
		t.Fatalf("handoff prompt does not end with the instruction: %q", relayed)
	}

	// A human mention is not a handoff: nothing is withheld there, because the
	// thread is the only place that context exists.
	human := b.buildPromptWithThreadContext(imbot.Message{SenderName: "韩音", Text: "再看看"}, history)
	if !strings.Contains(human, "long progress narration one") ||
		!strings.Contains(human, "long progress narration two") {
		t.Fatalf("human-triggered prompt withheld thread context: %q", human)
	}
}

func TestBuildPromptWithThreadContextKeepsInstructionWhenOnlyAgentProseWasBackfilled(t *testing.T) {
	b := &IMBridge{Runtime: &service.Runtime{}}
	history := []imbot.Message{{SenderName: "Ada", Text: "narration", FromBot: true}}

	prompt := b.buildPromptWithThreadContext(
		imbot.Message{SenderName: "Ada", Text: "请复审新候选", FromBot: true}, history)

	// The note still has to appear: without it the receiving agent cannot tell
	// that the thread holds more than the single line it was handed.
	if !strings.Contains(prompt, fmt.Sprintf(imThreadContextRelayedTextf, 1)) {
		t.Fatalf("prompt silently hid the omitted agent message: %q", prompt)
	}
	if strings.Contains(prompt, "narration") {
		t.Fatalf("prompt repeated the other agent's prose: %q", prompt)
	}
}

// The prompt withholds the other agent's prose; the mirrored conversation must
// not. The web view is where the complete thread is read, so persistence and
// prompting deliberately diverge here.
func TestHandoffTurnStillPersistsTheOtherAgentsMessages(t *testing.T) {
	ms := storetest.New()
	cfg := agenttest.Config()
	agentUser := store.User{
		ID: "agent1", Name: "Guard", OwnerID: "owner1", WorkDir: t.TempDir(),
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}
	ms.Users = []store.User{{
		ID: "owner1", Username: "owner", Name: "Owner",
		ProviderBindings: map[string]string{config.CLITypeClaude: "acc1"},
	}, agentUser}
	ms.Bots = []store.Bot{{
		ID: "bot1", AgentID: "agent1", Platform: "slack", Enabled: true,
		Channels: `[{"channel":"*","require_mention":true,"allow_bot_mentions":true}]`,
	}}
	rec := &agenttest.CallRecord{}
	b := &IMBridge{
		Runtime: &service.Runtime{Store: ms, Cfg: cfg, Pool: service.NewPool(cfg), Drainer: service.NewDrainer(), Broadcaster: service.NewBroadcaster(), UserHub: service.NewUserHub(), Backends: service.NewBackendRegistry(nil, agenttest.ScriptedBackend{Result: "reviewed", Rec: rec})}, Bots: ms,
	}
	msg := imbot.Message{
		Platform: "slack", AgentID: "agent1", BotID: "bot1", ChannelID: "C1",
		ThreadID: "171.1", MessageID: "C1|171.3", SenderName: "Ada",
		Text: "请审核 MR 3340", Mentioned: true, FromBot: true,
		LoadThreadMessages: func(_ context.Context, _ string) ([]imbot.Message, error) {
			return []imbot.Message{
				{Platform: "slack", MessageID: "C1|171.2", SenderName: "Ada", Text: "候选已固定，测试 5/5 通过", FromBot: true},
			}, nil
		},
	}

	b.HandleMessage(context.Background(), msg, &imbottest.ReplyRecorder{})

	var mirrored []store.Message
	for _, conv := range ms.Conversations {
		mirrored = append(mirrored, ms.Messages[conv.ID]...)
	}
	found := false
	for _, m := range mirrored {
		if strings.Contains(m.Content, "候选已固定，测试 5/5 通过") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the other agent's message is missing from the mirrored conversation: %+v", mirrored)
	}
	if len(rec.Prompts) != 1 {
		t.Fatalf("agent prompts = %v, want exactly one", rec.Prompts)
	}
	if strings.Contains(rec.Prompts[0], "候选已固定") {
		t.Fatalf("prompt repeated the other agent's prose: %q", rec.Prompts[0])
	}
}

func numberedMessages(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("m%d", i)
	}
	return out
}
