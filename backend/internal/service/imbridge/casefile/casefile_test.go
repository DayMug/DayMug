package casefile

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

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

// A dropped 「已排除」 entry comes back, and the rest of the turn's work stays.
// Rejecting the whole document instead — the original behaviour — meant one
// dropped line silently discarded everything else the turn had written.
func TestReconcileRestoresDroppedExcludedItemsAndKeepsTheRestOfTheUpdate(t *testing.T) {
	before := validCaseDoc("- 已确认（a.go:1）", "- 不是旧仓库（repo.txt:3）\n- 不是精度公式（probe.json:8）", "- 无", "- 无", "- a.go")
	after := validCaseDoc("- 已确认（a.go:1）\n- 新结论（b.go:9）", "- 不是旧仓库（repo.txt:3）", "- 无", "- 无", "- a.go")

	got, err := ReconcileAgentUpdate(before, after)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !strings.Contains(got, "- 不是精度公式（probe.json:8）") {
		t.Fatal("the dropped ruled-out item was not restored")
	}
	if !strings.Contains(got, "- 新结论（b.go:9）") {
		t.Fatal("the turn's other work was discarded along with the fix")
	}
	if len(sectionLines(got, "## 已排除")) != 2 {
		t.Fatalf("已排除 = %v, want exactly the two entries", sectionLines(got, "## 已排除"))
	}
}

// "暂无。" is a placeholder, not an exclusion. Protecting it made it immortal:
// writing the first real ruled-out item had to replace it, which read as
// deleting a protected entry and cost the turn its entire case update.
func TestReconcileLetsARealEntryReplaceAnExclusionPlaceholder(t *testing.T) {
	before := validCaseDoc("- 已确认（a.go:1）", "暂无。", "- 无", "- 无", "- a.go")
	after := validCaseDoc("- 已确认（a.go:1）", "- 不是缓存（probe.log:2）", "- 无", "- 无", "- a.go")

	got, err := ReconcileAgentUpdate(before, after)
	if err != nil {
		t.Fatalf("replacing a placeholder must be allowed: %v", err)
	}
	if strings.Contains(got, "暂无。") {
		t.Fatal("the placeholder was resurrected")
	}
}

// Section drift is still refused — but only for a document that was well-formed
// to begin with. Cases written before the fixed-six-section contract are in the
// store malformed, and refusing every edit that has not fully migrated them
// leaves them stuck forever, re-emitting the same "结构不合规" nag every turn.
func TestReconcileRejectsSectionDriftOnlyWhenTheDocumentWasWellFormed(t *testing.T) {
	wellFormed := validCaseDoc("- 已确认（a.go:1）", "- 不是旧仓库（repo.txt:3）", "- 无", "- 无", "- a.go")
	if _, err := ReconcileAgentUpdate(wellFormed, wellFormed+"\n## 发布记录\n- done\n"); err == nil {
		t.Fatal("adding a seventh section to a well-formed case must be rejected")
	}

	legacy := wellFormed + "\n## 历史修复\n- 旧条目\n"
	progress := wellFormed + "\n## 历史修复\n"
	if _, err := ReconcileAgentUpdate(legacy, progress); err != nil {
		t.Fatalf("a partial migration of an already-drifted case must be allowed: %v", err)
	}
}

// Merging needs somewhere to merge into. A document that deleted the section
// outright cannot carry the entries forward, so it is refused rather than
// stored with them lost.
func TestReconcileRejectsAnUpdateThatRemovesTheExcludedSection(t *testing.T) {
	before := validCaseDoc("- 已确认（a.go:1）", "- 不是旧仓库（repo.txt:3）", "- 无", "- 无", "- a.go")
	after := strings.Replace(before, "## 已排除\n- 不是旧仓库（repo.txt:3）\n\n", "", 1)
	if _, err := ReconcileAgentUpdate(before, after); err == nil {
		t.Fatal("dropping the 已排除 section must be rejected")
	}
}

// Waiting until the soft limit caused cliff rewrites in production, so even a
// small case gets a compact incremental-maintenance reminder.
func TestCaseTurnReminderRequiresIncrementalMaintenanceUnderSoftLimit(t *testing.T) {
	got := TurnReminder("case.md", validCaseDoc("- A（a.go:1）", "- 无", "- 无", "- 无", "- a.go"))
	if !strings.Contains(got, "每轮结束前增量整理") {
		t.Fatalf("reminder = %q, want incremental maintenance below the soft limit", got)
	}
	over := TurnReminder("case.md", strings.Repeat("排", SoftTokenLimit+100))
	if over == "" {
		t.Fatal("a case past the soft limit produced no reminder")
	}
}

// Rotation has to be driven by the measured context reading, not by the agent
// volunteering that it finished a stage.
func TestContextOverThreshold(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		ratio   float64
		want    bool
	}{
		{"empty payload never rotates", "", 0, false},
		{"unparseable payload never rotates", "not json", 0, false},
		{"missing total never rotates", `{"used":200000,"total":0}`, 0, false},
		{"below the ratio holds the session", `{"used":100000,"total":258400}`, 0, false},
		{"at the ratio rotates", `{"used":180880,"total":258400}`, 0, true},
		{"far above the ratio rotates", `{"used":242592,"total":258400}`, 0, true},
		{"a 1M window uses the same ratio", `{"used":700000,"total":1000000}`, 0, true},
		{"a 1M window well under does not", `{"used":300000,"total":1000000}`, 0, false},
		{"a configured lower ratio rotates earlier", `{"used":150000,"total":258400}`, 0.55, true},
		{"the same reading holds at the default", `{"used":150000,"total":258400}`, 0, false},
		{"a configured higher ratio holds longer", `{"used":200000,"total":258400}`, 0.90, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContextOverThreshold(tt.payload, tt.ratio); got != tt.want {
				t.Fatalf("ContextOverThreshold(%q, %v) = %v, want %v", tt.payload, tt.ratio, got, tt.want)
			}
		})
	}
}

// Over the hard cap the agent must be told to compress by deleting before it
// does anything else — a model left to its own judgement appends forever.
func TestCaseSystemPromptDemandsCompressionOverHardLimit(t *testing.T) {
	small := SystemPrompt("/w/case.md", "# Case\n\n## 目标\n- 查清 A\n")
	if strings.Contains(small, "硬上限") && strings.Contains(small, "本轮第一件事") {
		t.Fatal("a small case must not be told to compress")
	}

	huge := SystemPrompt("/w/case.md", strings.Repeat("排除了缓存这条路径因为回源仍然复现。", 900))
	if !strings.Contains(huge, "本轮第一件事") {
		t.Fatalf("an over-cap case was not ordered to compress:\n%s", huge)
	}
	if !strings.Contains(huge, "「已排除」保留") {
		t.Fatal("the compression order must protect the ruled-out section")
	}
}

func TestCaseSystemPromptDefinesClosedSectionsAndArtifactOwnership(t *testing.T) {
	prompt := SystemPrompt("/home/alice/.daymug/agents/a1/case/case.md", validCaseDoc("- A（a.go:1）", "- 无", "- 无", "- 无", "- a.go"))
	for _, rule := range []string{
		"二级标题是封闭集合且顺序固定",
		"既有条目禁止删除、改写、改名",
		"/home/alice/.daymug/agents/a1/case/evidence/case/",
		"/home/alice/.daymug/agents/a1/case/artifacts/case/",
		"只审不做的 agent",
	} {
		if !strings.Contains(prompt, rule) {
			t.Fatalf("system prompt is missing platform rule %q", rule)
		}
	}
}

// The number shown to the agent decides whether it compresses, so it has to
// mean roughly the same thing in Chinese as in English.
func TestEstimateTokensAcrossScripts(t *testing.T) {
	if got := EstimateTokens(strings.Repeat("排", 100)); got != 100 {
		t.Fatalf("100 CJK runes = %d tokens, want ~100", got)
	}
	latin := EstimateTokens(strings.Repeat("a", 400))
	if latin < 90 || latin > 110 {
		t.Fatalf("400 latin chars = %d tokens, want ~100", latin)
	}
	if EstimateTokens("") != 0 {
		t.Fatal("empty text must cost nothing")
	}
}

func TestCaseCarriesWorkIgnoresTheUntouchedTemplate(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want bool
	}{
		{"no case at all", "", false},
		{"the template a first turn is handed", Template("Slack·C1"), false},
		{"headings only", "# Case: x\n\n## 目标\n\n## 已确立事实\n", false},
		{"one recorded fact", validCaseDoc("- A（a.go:1）", "", "", "", ""), true},
		{"only a closed-out section", validCaseDoc("", "", "", "无。", ""), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CarriesWork(tc.doc); got != tc.want {
				t.Fatalf("CarriesWork() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The template is what every first turn edits, so it has to satisfy the same
// structure check the agent's edits are held to — otherwise the first save of
// every thread would be judged against a malformed baseline.
func TestTemplateIsWellFormedAndCarriesNoWork(t *testing.T) {
	doc := Template("Slack·C1")
	if issues := documentIssues(doc); len(issues) != 0 {
		t.Fatalf("template has structure issues: %v", issues)
	}
	if CarriesWork(doc) {
		t.Fatal("the untouched template must not count as absorbed work")
	}
	if !strings.HasPrefix(doc, "# Case: Slack·C1\n") {
		t.Fatalf("template title = %q", strings.SplitN(doc, "\n", 2)[0])
	}
}

func TestAbsorbedSession(t *testing.T) {
	sessionStart := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	written := validCaseDoc("- A（a.go:1）", "", "", "", "")
	tests := []struct {
		name      string
		doc       string
		updatedAt time.Time
		want      bool
	}{
		{"an empty template never absorbed anything", Template("t"), sessionStart.Add(time.Hour), false},
		{"a case saved during the session absorbed it", written, sessionStart.Add(time.Minute), true},
		{"a case older than the session did not", written, sessionStart.Add(-time.Minute), false},
		{"a case saved at the very instant did not", written, sessionStart, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AbsorbedSession(tt.doc, tt.updatedAt, sessionStart); got != tt.want {
				t.Fatalf("AbsorbedSession() = %v, want %v", got, tt.want)
			}
		})
	}
}
