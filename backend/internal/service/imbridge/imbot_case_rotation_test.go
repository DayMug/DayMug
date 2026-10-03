package imbridge

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/service/imbridge/casefile"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// overRatio is a context reading past the default rotation ratio but below the
// force ceiling — the band the admission gate governs.
const overRatio = `{"used":164101,"total":258400}`

// The failure this gate exists for: a first turn burns past the ratio and never
// writes the case, so rotating would assemble the next session from an empty
// template and lose everything the turn found.
func TestAdmitRotationHoldsWhenTheCaseIsStillEmpty(t *testing.T) {
	r := caseRun(t, storetest.New(), t.TempDir())
	conv := store.Conversation{ID: "conv1", CreatedAt: time.Now().Add(-20 * time.Minute), LastContextUsage: overRatio}

	if r.admitRotation(context.Background(), conv) {
		t.Fatal("rotated onto an empty case: the retired session's work would be unrecoverable")
	}
	if !r.caseRotationHeld {
		t.Fatal("the hold was not recorded, so the agent is never told why its context is full")
	}
}

// A case written during the retired session's lifetime is the sound trade:
// the transcript is redundant with a document that already has its findings.
func TestAdmitRotationAllowsOnceTheCaseAbsorbedTheSession(t *testing.T) {
	fake := storetest.New()
	r := caseRun(t, fake, t.TempDir())
	if _, err := fake.SaveThreadCase(context.Background(), "C1", "T1",
		validCaseDoc("- 邮件只发管理员（api.go:31）", "", "", "", ""), "Ada"); err != nil {
		t.Fatalf("seed case: %v", err)
	}
	conv := store.Conversation{ID: "conv1", CreatedAt: time.Now().Add(-20 * time.Minute), LastContextUsage: overRatio}

	if !r.admitRotation(context.Background(), conv) {
		t.Fatal("a case that absorbed the session must not block rotation — that is what rotation is for")
	}
	if r.caseRotationHeld {
		t.Fatal("recorded a hold on a turn that rotated")
	}
}

// A case with content is not enough: an earlier session's case says nothing
// about what this one just spent a full window learning.
func TestAdmitRotationHoldsWhenTheCasePredatesTheSession(t *testing.T) {
	fake := storetest.New()
	r := caseRun(t, fake, t.TempDir())
	if _, err := fake.SaveThreadCase(context.Background(), "C1", "T1",
		validCaseDoc("- 旧结论（a.go:1）", "", "", "", ""), "Ada"); err != nil {
		t.Fatalf("seed case: %v", err)
	}
	conv := store.Conversation{ID: "conv2", CreatedAt: time.Now().Add(time.Minute), LastContextUsage: overRatio}

	if r.admitRotation(context.Background(), conv) {
		t.Fatal("rotated on a case older than the session it retired")
	}
}

// The hold cannot be permanent: a session too full to take another turn helps
// nobody, so at the ceiling the platform rotates and accepts the loss.
func TestAdmitRotationForcesAtTheCeiling(t *testing.T) {
	r := caseRun(t, storetest.New(), t.TempDir())
	conv := store.Conversation{
		ID:               "conv1",
		CreatedAt:        time.Now().Add(-20 * time.Minute),
		LastContextUsage: fmt.Sprintf(`{"used":%d,"total":258400}`, int(258400*(casefile.RotateForceRatio+0.02))),
	}

	if !r.admitRotation(context.Background(), conv) {
		t.Fatal("held a session past the force ceiling, which leaves the thread unable to answer at all")
	}
}

// A held turn starts on a context that is already past the ratio. The agent is
// the only party that can end the hold, so it has to be told what ends it.
func TestRotationHoldNoticeRidesOnTheResumedTurn(t *testing.T) {
	r := caseRun(t, storetest.New(), t.TempDir())
	r.caseDoc = validCaseDoc("- A（a.go:1）", "", "", "", "")
	r.caseRotationHeld = true

	opts := agent.RunRequest{IsResume: true}
	prompt := r.attachCase(&opts, "user asked something")

	if opts.SystemPrompt != "" {
		t.Fatalf("the hold notice must not enter the system prompt of a resumed session: %q", opts.SystemPrompt)
	}
	if !strings.Contains(prompt, "没有**轮换会话") {
		t.Fatalf("a held turn was not told why its context is full:\n%s", prompt)
	}
	if !strings.Contains(prompt, r.casePath()) {
		t.Fatal("the hold notice does not say which file ends the hold")
	}
}

// Rotation is only safe for state the case carries. A human's follow-up answers
// the agent's last message, and after rotation that text exists nowhere else —
// Slack's backfill drops the bot's own posts.
func TestHandoverExcerptRidesIntoTheSessionThatReplacesTheRotatedOne(t *testing.T) {
	fake := storetest.New()
	fake.Messages = map[string][]store.Message{
		"retired": {
			{ID: "m1", ConversationID: "retired", Role: "user", Content: "查一下"},
			{ID: "m2", ConversationID: "retired", Role: "assistant", Content: "候选邮箱：garen@a.com、info@b.com"},
			{ID: "m3", ConversationID: "retired", Role: "tool", Content: "{}"},
		},
	}
	r := caseRun(t, fake, t.TempDir())
	r.conversation = store.Conversation{ID: "fresh", WorkDir: r.agentUser.WorkDir}
	r.retiredConversationID = "retired"

	r.prepareCase(context.Background())
	opts := agent.RunRequest{}
	prompt := r.attachCase(&opts, "绝对不是第一个，其他几个你自己检查一下")

	if !strings.Contains(opts.SystemPrompt, "候选邮箱：garen@a.com、info@b.com") {
		t.Fatalf("the retired session's final reply did not reach the new session:\n%s", opts.SystemPrompt)
	}
	if !strings.Contains(opts.SystemPrompt, "<case>") {
		t.Fatal("the handover block replaced the case instead of accompanying it")
	}
	if prompt != "绝对不是第一个，其他几个你自己检查一下" {
		t.Fatalf("the handover leaked into the user message: %q", prompt)
	}
}

// A turn that did not rotate has nothing to hand over, and must not pay for a
// lookup or carry a stale excerpt.
func TestNoHandoverWithoutRotation(t *testing.T) {
	fake := storetest.New()
	fake.Messages = map[string][]store.Message{
		"other": {{ID: "m1", ConversationID: "other", Role: "assistant", Content: "不该出现"}},
	}
	r := caseRun(t, fake, t.TempDir())
	r.conversation = store.Conversation{ID: "conv1", WorkDir: r.agentUser.WorkDir}

	r.prepareCase(context.Background())
	if r.caseHandover != "" {
		t.Fatalf("carried a handover into a turn that did not rotate: %q", r.caseHandover)
	}
}

// The thread's first conversation is unnumbered; each rotation continues the
// retired title under the replacement's number instead of re-summarizing.
func TestRotatedConversationTitle(t *testing.T) {
	const fallback = "Slack·general"
	cases := []struct {
		name        string
		retired     string
		retiredSeq  int
		want        string
		replaceable bool
	}{
		{"first rotation numbers 2", "Slack·排查邮件退信", 1, "Slack·排查邮件退信-2", false},
		{"later rotations count up", "Slack·排查邮件退信-2", 2, "Slack·排查邮件退信-3", false},
		{"counts past single digits", "Slack·排查邮件退信-9", 9, "Slack·排查邮件退信-10", false},
		{"digits in the first title are not a number", "Slack·COVID-19", 1, "Slack·COVID-19-2", false},
		{"only the known suffix is stripped", "Slack·COVID-19-2", 2, "Slack·COVID-19-3", false},
		{"a renamed conversation keeps the new name", "发布排查", 3, "发布排查-4", false},
		{"untitled first conversation stays provisional", fallback, 1, fallback + "-2", true},
		{"untitled rotated conversation stays provisional", fallback + "-2", 2, fallback + "-3", true},
		{"empty title falls back", "", 1, fallback + "-2", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, replaceable := rotatedConversationTitle(tc.retired, tc.retiredSeq, fallback)
			if got != tc.want || replaceable != tc.replaceable {
				t.Fatalf("rotatedConversationTitle(%q, %d) = %q, %t; want %q, %t",
					tc.retired, tc.retiredSeq, got, replaceable, tc.want, tc.replaceable)
			}
		})
	}
}

// A conversation's number comes from how many rotations stand behind it.
func TestConversationSeqWalksTheRotationChain(t *testing.T) {
	fake := storetest.New()
	fake.RotatedFrom = map[string]string{"c3": "c2", "c2": "c1"}
	r := caseRun(t, fake, t.TempDir())

	for id, want := range map[string]int{"c1": 1, "c2": 2, "c3": 3} {
		if got := r.conversationSeq(context.Background(), id); got != want {
			t.Errorf("conversationSeq(%s) = %d, want %d", id, got, want)
		}
	}
}

// A looping chain must end the walk rather than hang the turn.
func TestConversationSeqStopsOnALoop(t *testing.T) {
	fake := storetest.New()
	fake.RotatedFrom = map[string]string{"a": "b", "b": "a"}
	r := caseRun(t, fake, t.TempDir())

	if got := r.conversationSeq(context.Background(), "a"); got != maxRotationChain {
		t.Fatalf("conversationSeq on a loop = %d, want the cap %d", got, maxRotationChain)
	}
}

// The replacement is created with the numbered title and linked to the
// conversation it replaced, which is what numbers the next rotation.
func TestEnsureConversationTitlesAndLinksTheReplacement(t *testing.T) {
	fake := storetest.New()
	r := caseRun(t, fake, t.TempDir())

	conv, created, err := r.bridge.ensureConversation(context.Background(), r.msg, r.agentUser, store.BotThread{}, "Slack·排查邮件退信-2", "retired", service.ConversationSeed{})
	if err != nil || !created {
		t.Fatalf("ensureConversation: created=%t err=%v", created, err)
	}
	if conv.Title != "Slack·排查邮件退信-2" {
		t.Fatalf("title = %q, want the rotated title", conv.Title)
	}
	if got := r.conversationSeq(context.Background(), conv.ID); got != 2 {
		t.Fatalf("replacement is conversation %d on its thread, want 2", got)
	}
}
