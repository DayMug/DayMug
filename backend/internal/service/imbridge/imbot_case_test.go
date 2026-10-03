package imbridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/service/imbridge/casefile"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// caseRun builds a minimal imRun wired to a fake store, in case-file mode, with
// its case directory resolved the way resolveTarget does in production.
//
// The agent's identity is derived from homeRoot so that two calls sharing a
// root describe the same agent (a second turn) while two calls with different
// roots describe different ones — which is what the tests below vary.
func caseRun(t *testing.T, fake *storetest.Fake, homeRoot string) *imRun {
	t.Helper()
	key := service.SanitizeUploadSegment(homeRoot)
	owner := store.User{ID: "owner-" + key, Username: "u-" + key, Email: key + "@example.com", WorkDir: homeRoot}
	agentUser := store.User{
		ID:       "agent-" + key,
		Name:     "Ada",
		OwnerID:  owner.ID,
		Email:    owner.Email,
		WorkDir:  homeRoot,
		CaseMode: true,
	}
	for _, u := range []store.User{owner, agentUser} {
		if !slices.ContainsFunc(fake.Users, func(existing store.User) bool { return existing.ID == u.ID }) {
			fake.Users = append(fake.Users, u)
		}
	}
	home, caseRoot, err := agentCaseRoot(context.Background(), fake, agentUser)
	if err != nil {
		t.Fatalf("agentCaseRoot: %v", err)
	}
	return &imRun{
		bridge:      &IMBridge{Runtime: &service.Runtime{Store: fake}},
		msg:         imbot.Message{Platform: imbot.PlatformSlack, ChannelID: "C1", ThreadID: "T1"},
		imTarget:    imTarget{agentUser: agentUser},
		imCaseState: imCaseState{homeRoot: home, caseRoot: caseRoot},
	}
}

func validCaseDoc(facts, excluded, hypotheses, open, artifacts string) string {
	return fmt.Sprintf(`# Case: test

## 目标
- 完成测试

## 已确立事实
%s

## 已排除
%s

## 当前假设
%s

## 未闭环
%s

## 产物坐标
%s
`, facts, excluded, hypotheses, open, artifacts)
}

// The toggle is only half the gate: an agent with case mode on still serves web
// conversations, which have no IM thread to key a case by.
func TestCaseModeRequiresBothToggleAndChannel(t *testing.T) {
	base := caseRun(t, storetest.New(), t.TempDir())
	if !base.caseModeEnabled() {
		t.Fatal("an IM turn for a case-mode agent should be in case mode")
	}

	off := caseRun(t, storetest.New(), t.TempDir())
	off.agentUser.CaseMode = false
	if off.caseModeEnabled() {
		t.Fatal("case mode must stay off for an agent that did not opt in")
	}

	web := caseRun(t, storetest.New(), t.TempDir())
	web.msg.ChannelID = ""
	if web.caseModeEnabled() {
		t.Fatal("a conversation with no IM channel must not trigger case mode")
	}
}

func TestUnboundedTurnFollowsCaseModeSetting(t *testing.T) {
	base := caseRun(t, storetest.New(), t.TempDir())
	if !base.unboundedTurn() {
		t.Fatal("a case-mode IM turn should skip the consumption guardrail")
	}

	// A case directory that failed to resolve must not reinstate the guardrail.
	noRoot := caseRun(t, storetest.New(), t.TempDir())
	noRoot.caseRoot = ""
	if !noRoot.unboundedTurn() {
		t.Fatal("unbounded must follow the setting, not the case directory")
	}

	off := caseRun(t, storetest.New(), t.TempDir())
	off.agentUser.CaseMode = false
	if off.unboundedTurn() {
		t.Fatal("an agent without case mode keeps the guardrail")
	}

	web := caseRun(t, storetest.New(), t.TempDir())
	web.msg.ChannelID = ""
	if web.unboundedTurn() {
		t.Fatal("a turn with no IM channel keeps the guardrail")
	}
}

// A thread with no case yet has to get the section skeleton, otherwise the very
// first turn has nowhere to write and the mechanism never starts.
func TestPrepareCaseSeedsTemplateOnFirstTurn(t *testing.T) {
	workDir := t.TempDir()
	r := caseRun(t, storetest.New(), workDir)
	r.prepareCase(context.Background())

	if r.caseDoc == "" {
		t.Fatal("first turn got no case document")
	}
	for _, heading := range []string{"## 目标", "## 已确立事实", "## 已排除", "## 当前假设", "## 未闭环", "## 产物坐标"} {
		if !strings.Contains(r.caseDoc, heading) {
			t.Fatalf("seeded case is missing %q", heading)
		}
	}
	if _, err := os.Stat(r.casePath()); err != nil {
		t.Fatalf("case was not materialized on disk: %v", err)
	}
}

// The turn edits a file; the store is what outlives the session. If the
// read-back does not persist, rotating the session silently discards the work.
func TestPersistCaseStoresWhatTheTurnWrote(t *testing.T) {
	fake := storetest.New()
	workDir := t.TempDir()
	r := caseRun(t, fake, workDir)
	r.prepareCase(context.Background())

	edited := validCaseDoc("- 已复现（logs/probe.txt:1）", "- 不是 CDN 缓存，回源头也复现（logs/probe.txt:12）", "- 无", "- 无", "- logs/probe.txt")
	if err := os.WriteFile(r.casePath(), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	r.persistCase(context.Background(), r.caseDoc)

	got, err := fake.GetThreadCase(context.Background(), "C1", "T1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Doc != edited {
		t.Fatalf("stored doc = %q, want the edited document", got.Doc)
	}
	if got.Version != 1 {
		t.Fatalf("version = %d, want 1", got.Version)
	}
	if got.UpdatedBy != "Ada" {
		t.Fatalf("updated_by = %q, want the agent that wrote it", got.UpdatedBy)
	}
}

// An untouched file must not burn a revision — otherwise history fills with
// duplicates and the rollback path becomes useless.
func TestPersistCaseSkipsUnchangedDocument(t *testing.T) {
	fake := storetest.New()
	r := caseRun(t, fake, t.TempDir())
	r.prepareCase(context.Background())
	r.persistCase(context.Background(), r.caseDoc)

	got, _ := fake.GetThreadCase(context.Background(), "C1", "T1")
	if got.Version != 0 {
		t.Fatalf("version = %d, want 0 — an unedited case is not a revision", got.Version)
	}
}

func TestPersistCaseRejectsSectionDriftAndRestoresLastSafeDocument(t *testing.T) {
	fake := storetest.New()
	r := caseRun(t, fake, t.TempDir())
	r.prepareCase(context.Background())

	drifted := validCaseDoc("- 已确认（a.go:1）", "- 不是缓存（probe.log:2）", "- 无", "- 无", "- a.go") +
		"\n## 2026-08-19 MR 审核\n- 已通过\n"
	if err := os.WriteFile(r.casePath(), []byte(drifted), 0o644); err != nil {
		t.Fatal(err)
	}
	r.persistCase(context.Background(), r.caseDoc)

	stored, _ := fake.GetThreadCase(context.Background(), "C1", "T1")
	if stored.Version != 0 {
		t.Fatalf("unsafe document was persisted as version %d", stored.Version)
	}
	restored, err := os.ReadFile(r.casePath())
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != r.caseDoc {
		t.Fatal("rejected document was not restored to the last safe version")
	}
}

// A rollback the agent is not told about reads exactly like a failed write, so
// the next turn's cheapest move is to write the same rejected text again. The
// notice has to survive into that turn and then clear itself.
func TestRejectedUpdateTellsTheNextTurnWhyAndClearsAfterwards(t *testing.T) {
	fake := storetest.New()
	workDir := t.TempDir()
	r := caseRun(t, fake, workDir)
	r.prepareCase(context.Background())

	drifted := validCaseDoc("- 已确认（a.go:1）", "- 无", "- 无", "- 无", "- a.go") +
		"\n## 2026-08-19 MR 审核\n- 已通过\n"
	if err := os.WriteFile(r.casePath(), []byte(drifted), 0o644); err != nil {
		t.Fatal(err)
	}
	r.persistCase(context.Background(), r.caseDoc)

	next := caseRun(t, fake, workDir)
	next.prepareCase(context.Background())
	if next.caseRejection == "" {
		t.Fatal("the next turn was not told its predecessor's update was rolled back")
	}
	opts := &agent.RunRequest{IsResume: true}
	if prompt := next.attachCase(opts, "继续"); !strings.Contains(prompt, "被平台驳回") {
		t.Fatalf("prompt does not carry the rejection notice: %s", prompt)
	}

	third := caseRun(t, fake, workDir)
	third.prepareCase(context.Background())
	if third.caseRejection != "" {
		t.Fatal("the notice repeated on a later turn; it must be delivered once")
	}
}

// A save that never reached the store must not come back as a human edit. The
// file is left ahead of the sync marker either way, so without a marker of its
// own the agent's retry is filed under "human" — and a human edit is the one
// correction channel the design treats as authoritative.
func TestASaveThatFailedIsRetriedUnderTheAgentsOwnName(t *testing.T) {
	fake := storetest.New()
	workDir := t.TempDir()
	r := caseRun(t, fake, workDir)
	r.prepareCase(context.Background())

	written := validCaseDoc("- 已确认（a.go:1）", "- 不是缓存（probe.log:2）", "- 无", "- 无", "- a.go")
	if err := os.WriteFile(r.casePath(), []byte(written), 0o644); err != nil {
		t.Fatal(err)
	}
	fake.SaveThreadCaseErr = errors.New("database is locked (5) (SQLITE_BUSY)")
	r.persistCase(context.Background(), r.caseDoc)

	fake.SaveThreadCaseErr = nil
	next := caseRun(t, fake, workDir)
	next.prepareCase(context.Background())

	stored, err := fake.GetThreadCase(context.Background(), "C1", "T1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Doc != written {
		t.Fatal("the dropped case was not recovered on the next turn")
	}
	if stored.UpdatedBy != "Ada" {
		t.Fatalf("updated_by = %q, want the agent that actually wrote it", stored.UpdatedBy)
	}
	if _, err := os.Stat(next.caseFile().UnsavedPath()); !os.IsNotExist(err) {
		t.Fatal("the unsaved marker outlived the recovery")
	}
}

// Two agents on one thread must land on one case: that shared document is the
// whole reason a handoff does not need a transcript replay.
func TestCaseIsSharedAcrossAgentsOnOneThread(t *testing.T) {
	fake := storetest.New()

	ada := caseRun(t, fake, t.TempDir())
	ada.prepareCase(context.Background())
	written := validCaseDoc("- MR 已创建（https://git/example/-/merge_requests/1）", "- 无", "- 无", "- 无", "- MR https://git/example/-/merge_requests/1 @ abc123")
	if err := os.WriteFile(ada.casePath(), []byte(written), 0o644); err != nil {
		t.Fatal(err)
	}
	ada.persistCase(context.Background(), ada.caseDoc)

	guard := caseRun(t, fake, t.TempDir())
	guard.agentUser.ID, guard.agentUser.Name = "agent2", "Guard"
	guard.prepareCase(context.Background())

	if guard.caseDoc != written {
		t.Fatalf("second agent's case = %q, want the first agent's document", guard.caseDoc)
	}
}

// A human correcting the case file between turns is the strongest steering this
// design offers. Re-materializing the stored copy over it would silently throw
// that correction away.
func TestPrepareCaseAdoptsHumanEditMadeBetweenTurns(t *testing.T) {
	fake := storetest.New()
	r := caseRun(t, fake, t.TempDir())

	// Turn one writes a case.
	r.prepareCase(context.Background())
	agentDoc := validCaseDoc("- 已复现（logs/probe.txt:1）", "- 无", "- 怀疑是 CDN 缓存", "- 无", "- logs/probe.txt")
	if err := os.WriteFile(r.casePath(), []byte(agentDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	r.persistCase(context.Background(), r.caseDoc)

	// A human opens the file and deletes the wrong hypothesis.
	corrected := "# Case\n\n## 已排除\n- 不是 CDN 缓存，回源仍复现（logs/probe.txt:12）\n"
	if err := os.WriteFile(r.casePath(), []byte(corrected), 0o644); err != nil {
		t.Fatal(err)
	}

	// Turn two must start from the corrected text, and it must reach the store
	// so the other agent on the thread sees it too.
	next := caseRun(t, fake, r.workDir())
	next.prepareCase(context.Background())
	if next.caseDoc != corrected {
		t.Fatalf("case = %q, want the human-corrected document", next.caseDoc)
	}
	stored, _ := fake.GetThreadCase(context.Background(), "C1", "T1")
	if stored.Doc != corrected {
		t.Fatalf("stored doc = %q, want the human correction promoted to a revision", stored.Doc)
	}
	if stored.UpdatedBy != "human" {
		t.Fatalf("updated_by = %q, want the edit attributed to a human", stored.UpdatedBy)
	}
}

// A local copy that is merely stale — the peer agent saved a newer revision —
// must lose to the store, or one agent's work would overwrite the other's.
func TestPrepareCasePrefersStoreWhenLocalCopyIsStale(t *testing.T) {
	fake := storetest.New()
	ada := caseRun(t, fake, t.TempDir())
	ada.prepareCase(context.Background())
	firstDoc := validCaseDoc("- A 已确认（a.go:1）", "- 无", "- 无", "- 无", "- a.go")
	if err := os.WriteFile(ada.casePath(), []byte(firstDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	ada.persistCase(context.Background(), ada.caseDoc)

	// Guard, in its own work_dir, advances the case.
	guard := caseRun(t, fake, t.TempDir())
	guard.agentUser.Name = "Guard"
	guard.prepareCase(context.Background())
	peerDoc := validCaseDoc("- 复现路径确认（a.go:10）", "- 无", "- 无", "- 无", "- a.go")
	if err := os.WriteFile(guard.casePath(), []byte(peerDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	guard.persistCase(context.Background(), guard.caseDoc)

	// Ada's local file is now behind. It must be replaced, not adopted.
	back := caseRun(t, fake, ada.workDir())
	back.prepareCase(context.Background())
	if back.caseDoc != peerDoc {
		t.Fatalf("case = %q, want the peer's newer revision", back.caseDoc)
	}
}

// The case is a document the agent rewrites, and both adapters render the
// system prompt at the very front. Re-injecting it on every turn would change
// the prefix on every turn and invalidate the whole cached conversation behind
// it — so it belongs to session assembly, not to each turn.
func TestAttachCaseInjectsFullDocumentOnlyWhenOpeningASession(t *testing.T) {
	r := caseRun(t, storetest.New(), t.TempDir())
	r.prepareCase(context.Background())

	fresh := agent.RunRequest{IsResume: false}
	prompt := r.attachCase(&fresh, "user asked something")
	if !strings.Contains(fresh.SystemPrompt, "## 案卷模式") {
		t.Fatal("a fresh session did not receive the case block")
	}
	if !strings.Contains(fresh.SystemPrompt, "<case>") {
		t.Fatal("a fresh session did not receive the case contents")
	}
	if prompt != "user asked something" {
		t.Fatalf("prompt = %q, want the user message untouched on a fresh session", prompt)
	}

	resumed := agent.RunRequest{IsResume: true}
	prompt = r.attachCase(&resumed, "user asked something")
	if resumed.SystemPrompt != "" {
		t.Fatalf("a resumed turn put %q in the system prompt, which invalidates the cached prefix", resumed.SystemPrompt)
	}
	if !strings.Contains(prompt, "每轮结束前增量整理") {
		t.Fatalf("a resumed turn did not receive the incremental-maintenance reminder: %q", prompt)
	}
}

// Enforcement still has to reach a resumed turn — but at the END of the
// prompt, where varying it per turn costs only its own tokens.
func TestAttachCaseRemindsAtPromptEndWhenResumedAndOverLimit(t *testing.T) {
	r := caseRun(t, storetest.New(), t.TempDir())
	r.prepareCase(context.Background())
	r.caseDoc = strings.Repeat("排除了缓存这条路径因为回源仍然复现。", 1200)

	resumed := agent.RunRequest{IsResume: true}
	prompt := r.attachCase(&resumed, "user asked something")
	if resumed.SystemPrompt != "" {
		t.Fatalf("over-limit reminder leaked into the system prompt: %q", resumed.SystemPrompt)
	}
	if !strings.HasPrefix(prompt, "user asked something") {
		t.Fatal("the reminder must follow the user message, not precede it")
	}
	if !strings.Contains(prompt, "本轮第一件事") {
		t.Fatalf("a resumed turn over the hard cap was not ordered to compress:\n%s", prompt)
	}
	if casefile.EstimateTokens(strings.TrimPrefix(prompt, "user asked something")) > 200 {
		t.Fatal("the reminder is supposed to be a few dozen tokens, not the whole document")
	}
}

// Case mode off, or no case document, must leave both the system prompt and the
// user message exactly as they were.
func TestAttachCaseIsInertWhenCaseModeIsOff(t *testing.T) {
	r := caseRun(t, storetest.New(), t.TempDir())
	r.agentUser.CaseMode = false

	opts := agent.RunRequest{}
	prompt := r.attachCase(&opts, "hello")
	if opts.SystemPrompt != "" || prompt != "hello" {
		t.Fatalf("attachCase touched a non-case-mode turn: system=%q prompt=%q", opts.SystemPrompt, prompt)
	}
}

// An IMBridge built without a Config still has to rotate: the fallback is what
// keeps case mode working for embedders and tests, and its absence would look
// like "rotation is off" rather than like a missing dependency.
func TestCaseRotateRatioFallsBackWithoutConfig(t *testing.T) {
	if got := (&IMBridge{Runtime: &service.Runtime{}}).caseRotateRatio(); got != casefile.RotateContextRatio {
		t.Fatalf("caseRotateRatio() = %v, want the built-in %v", got, casefile.RotateContextRatio)
	}
	b := &IMBridge{Runtime: &service.Runtime{Cfg: &config.Config{CaseRotateContextRatio: 0.55}}}
	if got := b.caseRotateRatio(); got != 0.55 {
		t.Fatalf("caseRotateRatio() = %v, want the configured 0.55", got)
	}
}

// The case path is built from connector-supplied ids, so it must not be able to
// escape the agent's case directory.
func TestCasePathStaysInsideTheAgentScope(t *testing.T) {
	homeRoot := t.TempDir()
	r := caseRun(t, storetest.New(), homeRoot)
	r.msg.ChannelID = "../../etc"
	r.msg.ThreadID = "../passwd"

	path := r.casePath()
	within, err := service.PathWithin(r.caseRoot, path)
	if err != nil || !within {
		t.Fatalf("case path %q escaped the case dir %q (err %v)", path, r.caseRoot, err)
	}
}

// The case belongs to the agent, not to whichever project the conversation is
// running in — so an agent whose work_dir is a subdirectory of its owner's home
// must still write its case up at the home root.
func TestCaseLandsInTheOwnerHomeNotTheConversationWorkDir(t *testing.T) {
	homeRoot := t.TempDir()
	r := caseRun(t, storetest.New(), homeRoot)
	project := filepath.Join(homeRoot, "some-project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	r.agentUser.WorkDir = project

	if _, err := r.materializeCase(validCaseDoc("- A（a.go:1）", "", "", "", ""), 1); err != nil {
		t.Fatalf("materializeCase: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, service.DaymugDir)); !os.IsNotExist(err) {
		t.Fatalf("a .daymug directory appeared in the project dir (stat err %v)", err)
	}
	scope, err := service.AgentScopeDir(r.agentUser.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := service.DaymugPath(homeRoot, scope+"/"+service.CaseSegment)
	if filepath.Dir(r.casePath()) != want {
		t.Fatalf("case path = %q, want a file in %q", r.casePath(), want)
	}
}
