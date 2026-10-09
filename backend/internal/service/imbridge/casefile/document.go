// Package casefile holds the rules of IM case-file mode that do not depend on
// a running turn: the shape of the case document, the limits it is held to,
// the instructions an agent is given about it, the reconciliation applied to
// an agent's edit, the rotation predicates, and the on-disk marker protocol
// beside the case file.
//
// Case-file mode replaces "one IM thread, one CLI session that grows until it
// is compacted" with "one IM thread, one bounded document that outlives every
// session". The document — the case — is what a fresh session is assembled
// from, so the cost of a turn stops scaling with how many turns came before it.
//
// Three properties do the work, and each is enforced by code rather than asked
// for in a prompt, because the prompt-only version of all three has already
// been measured to fail under load:
//
//   - The case is REWRITABLE. A transcript can only be appended to, so an
//     early hypothesis keeps exerting pull long after it was disproved. A
//     document can have that line deleted.
//   - The case is CAPPED. Over the cap the agent is told to compress by
//     deleting, because a model left to itself appends and never removes — and
//     deletion is the entire value of the mechanism.
//   - The session is ROTATED on a measured threshold, not on the agent's own
//     judgement of when it has "finished a stage".
//
// The turn orchestration — loading from and saving to the store, deciding
// where in the request the case goes — stays in package imbridge, which calls
// into this package for every decision that can be made from text alone.
package casefile

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

const (
	// SoftTokenLimit is where the agent is asked to start compressing.
	SoftTokenLimit = 8000
	// HardTokenLimit is where compression stops being advice: the next turn
	// is told, as its first instruction, to cut the case down before doing
	// anything else.
	//
	// Sizing, against the two windows this runs on (codex gpt-5.6 at ~258k,
	// Claude Opus at 1M): 12k is 4.6% and 1.2% of them respectively, and it is
	// paid once per session rather than once per turn (see imbridge's
	// attachCase). The cap is therefore not set by what the window can afford
	// — it is set by how much curation pressure is useful. 12k is roughly
	// 400-500 lines of pointer-carrying notes: still several times tighter
	// than a transcript summary of the same work, but with room for the long
	// tail of ruled-out branches a genuinely repetitive problem accumulates.
	//
	// Erring small is the more expensive mistake. Squeezed for space a model
	// drops the "ruled out" section first — it reads as the least load-bearing
	// and is in fact the most, because it is the only thing that stops the
	// next session from re-walking a dead end. The gap to the soft limit is
	// deliberately wide for the same reason: compression should start as
	// housekeeping while there is room to do it well, not as an emergency.
	HardTokenLimit = 12000
)

// HandoverExcerptRunes caps the retired session's final reply when it is
// carried into the session that replaces it. Two thousand runes is a couple of
// long paragraphs — enough for a conclusion plus the list of things it asked
// the human to choose between, which is the shape that actually gets replied
// to, and small enough that it cannot crowd out the case itself.
const HandoverExcerptRunes = 2000

// excludedHeading is the one section whose existing entries an agent may not
// drop: it is the only thing that stops the next session re-walking a dead end.
const excludedHeading = "## 已排除"

var sectionHeadings = []string{
	"## 目标",
	"## 已确立事实",
	excludedHeading,
	"## 当前假设",
	"## 未闭环",
	"## 产物坐标",
}

// sectionTemplate is what a thread with no case yet starts from.
//
// The six headings are the scenario-agnostic part of this design: they describe
// the state of an *investigation*, not the state of a bug fix or a deployment.
// The same shape holds for a data question, a migration, or a research task,
// which is why DayMug can own them without knowing what any given thread is
// about.
const sectionTemplate = `# Case: %s

## 目标
<还没有记录。第一次写案卷时填这里。>

## 已确立事实
<每条一行，必须带证据坐标（文件:行 / URL / SHA / 查询语句）。>

## 已排除
<排除了什么 + 为什么。这一节是防止反复的唯一手段，压缩时最后才动它。>

## 当前假设
<可以整条删除。被推翻就删掉，不要保留"我曾经以为"。>

## 未闭环
<待办 + 卡在谁手里。>

## 产物坐标
<路径、MR、SHA、验收 URL。只放指针，不要放内容。>
`

// Template is the empty six-section case a thread's first turn is handed.
func Template(title string) string {
	return fmt.Sprintf(sectionTemplate, title)
}

// CarriesWork reports whether the case holds anything a fresh session could
// be assembled from, as opposed to the untouched six-heading template.
//
// Headings and `<...>` placeholder lines are what a thread that has never had
// a case is materialized with, so neither counts as content. Anything else
// does, deliberately including a bare "无。" — an agent that wrote that engaged
// with the case, and second-guessing which sentences are "real" would make this
// predicate a quality judgement instead of the structural check it is.
func CarriesWork(doc string) bool {
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "<") {
			continue
		}
		return true
	}
	return false
}

func documentIssues(doc string) []string {
	got := headings(doc)
	issues := make([]string, 0, 2)
	if len(got) != len(sectionHeadings) {
		issues = append(issues, fmt.Sprintf("二级标题数量为 %d，应为 %d", len(got), len(sectionHeadings)))
	}
	limit := min(len(got), len(sectionHeadings))
	for i := 0; i < limit; i++ {
		if got[i] != sectionHeadings[i] {
			issues = append(issues, "二级标题必须严格使用固定六节及其顺序")
			break
		}
	}
	return issues
}

func headings(doc string) []string {
	var out []string
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "## ") {
			out = append(out, line)
		}
	}
	return out
}

func sectionLines(doc, heading string) []string {
	lines := strings.Split(doc, "\n")
	inside := false
	var content []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			if inside {
				break
			}
			inside = trimmed == heading
			continue
		}
		if inside && trimmed != "" && !strings.HasPrefix(trimmed, "<") {
			content = append(content, trimmed)
		}
	}
	return content
}

// placeholderEntry matches the "nothing here yet" line an agent writes into a
// section it has no content for.
//
// It exists because 「已排除」 entries are protected from deletion, and a
// placeholder is not an entry. Treating "暂无。" as one made it immortal: the
// first real exclusion the agent wrote had to replace it, that read as deleting
// a protected entry, and the turn lost its entire case update as a result —
// observed twice in production before this was fixed.
var placeholderEntry = regexp.MustCompile(`(?i)^[-*+>\s]*(暂无|尚无|无|none|n/?a)[。．.!！]?$`)

// protectedExclusions returns the 「已排除」 entries an update must carry
// forward — every line of the section except placeholders.
func protectedExclusions(doc string) []string {
	lines := sectionLines(doc, excludedHeading)
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if placeholderEntry.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}
	return kept
}

// ReconcileAgentUpdate returns the document that should be stored for an
// agent's edit, or an error when the edit cannot be stored at all.
//
// Reconciling rather than validating is the point. The previous version
// answered only yes or no, and a "no" reverted the whole file — so one dropped
// 「已排除」 line discarded everything else the turn had written, silently. Here
// the dropped entries are put back and the rest of the turn's work survives,
// which keeps the invariant without paying for it with the update.
//
// The structure check stays all-or-nothing, but only bites when the document
// was well-formed to begin with. Cases written before the fixed-six-section
// contract are still in the store malformed; refusing every edit that has not
// fully migrated them means they can never converge, which is exactly what the
// logs showed — weeks of turns re-reading the same "结构不合规" nag.
func ReconcileAgentUpdate(before, after string) (string, error) {
	if issues := documentIssues(after); len(issues) > 0 {
		if len(documentIssues(before)) == 0 {
			return "", errors.New(strings.Join(issues, "；"))
		}
	}
	present := make(map[string]struct{})
	for _, line := range sectionLines(after, excludedHeading) {
		present[line] = struct{}{}
	}
	var missing []string
	for _, line := range protectedExclusions(before) {
		if _, ok := present[line]; !ok {
			missing = append(missing, line)
		}
	}
	if len(missing) == 0 {
		return after, nil
	}
	merged, ok := appendSectionLines(after, excludedHeading, missing)
	if !ok {
		// Nowhere to put them back: the section the entries belong to is gone,
		// so storing this document would lose them for good.
		return "", fmt.Errorf("「已排除」章节缺失，无法保留既有条目：%s", missing[0])
	}
	return merged, nil
}

// appendSectionLines puts lines back at the end of a section's body, reporting
// false when the section is not in the document.
func appendSectionLines(doc, heading string, lines []string) (string, bool) {
	docLines := strings.Split(doc, "\n")
	start := -1
	for i, line := range docLines {
		if strings.TrimSpace(line) == heading {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false
	}
	end := len(docLines)
	for i := start + 1; i < len(docLines); i++ {
		if strings.HasPrefix(strings.TrimSpace(docLines[i]), "## ") {
			end = i
			break
		}
	}
	// Insert after the section's last non-blank line so the blank line that
	// separates it from the next heading stays where it is.
	insert := end
	for insert > start+1 && strings.TrimSpace(docLines[insert-1]) == "" {
		insert--
	}
	out := make([]string, 0, len(docLines)+len(lines))
	out = append(out, docLines[:insert]...)
	out = append(out, lines...)
	out = append(out, docLines[insert:]...)
	return strings.Join(out, "\n"), true
}

// EstimateTokens approximates the token cost of mixed CJK/latin text.
//
// A byte budget would be the cheaper thing to write, but the number here is
// shown to the agent and used to decide whether to demand compression, so it
// has to mean roughly the same thing in a Chinese case as in an English one:
// a CJK rune is about one token, latin text about four characters per token.
func EstimateTokens(s string) int {
	cjk, other := 0, 0
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			cjk++
			continue
		}
		other++
	}
	return cjk + (other+3)/4
}
