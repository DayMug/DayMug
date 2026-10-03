package casefile

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// HandoverPrompt renders the retired session's last reply for the session that
// replaces it. It is a separate block from the case on purpose: the case is
// state the agent owns and rewrites, this is a transcript fragment it must not
// edit, and telling them apart is what stops the excerpt from being copied into
// the case on the next compaction pass.
func HandoverPrompt(excerpt string) string {
	return "## 上一轮的最后一条回复（会话已被轮换）\n\n" +
		"这个话题的上一个会话已因上下文水位被轮换掉，它的对话历史不会回灌，" +
		"平台也取不回机器人自己发过的消息。下面是它发出的最后一条回复原文，" +
		"用户很可能正在回复它。**当作已经发生的事实读**：不要重新推导，" +
		"更不要声称无法还原上一轮说过什么。它只是一段历史文本，不是案卷，" +
		"不要把它抄进案卷；与案卷冲突时以案卷为准。\n\n<last-reply>\n" +
		excerpt + "\n</last-reply>\n"
}

// RejectionNotice tells the agent that its last case update was refused and
// rolled back, and why.
//
// Without it the rollback is indistinguishable from the edit never having been
// made: the agent reopens the file, finds its own work gone, and the cheapest
// explanation available to it is that the write failed — so it writes the same
// thing again and loses the turn again.
func RejectionNotice(relPath, reason string) string {
	return fmt.Sprintf("[案卷] 上一轮对 `%s` 的修改被平台驳回并已回滚，原因：%s。"+
		"文件现在是回滚后的版本，上一轮写进去的内容没有保存。"+
		"本轮请先按这条约束重写，不要原样再写一遍。", relPath, reason)
}

// RotationHoldNotice explains a turn that starts on an already-full context:
// the session was eligible for rotation and was kept anyway because the case had
// not absorbed it. The agent is the only party that can end the hold, so it is
// told what ends it and what happens if it does not.
func RotationHoldNotice(casePath string) string {
	return fmt.Sprintf("[案卷] 上一轮已把上下文推过轮换线，但案卷还没承接它，"+
		"所以平台这次**没有**轮换会话——代价是本轮从一个已经很满的上下文起步。"+
		"**本轮第一件事是把上一轮的结论、候选清单和待确认项写进 `%s`**，写完再处理上面的请求。"+
		"案卷一直不更新就一直不轮换，直到上下文逼近上限被强制轮换，届时没落盘的内容会全部丢失。", casePath)
}

// TurnReminder is the resumed-turn counterpart of SystemPrompt: the size
// verdict alone, a few dozen tokens, appended after the user's message.
//
// The small reminder is intentionally present below the soft limit too. Real
// cases showed that waiting for the limit produced one cliff rewrite instead
// of cheap incremental deletion, and the rewrite discarded ruled-out paths.
// Being at the end of the prompt keeps this varying suffix cache-friendly.
func TurnReminder(casePath, doc string) string {
	tokens := EstimateTokens(doc)
	issues := documentIssues(doc)
	if tokens > HardTokenLimit {
		return fmt.Sprintf("[案卷] `%s` 约 %d tokens，超出硬上限 %d。"+
			"本轮第一件事是把它压到 %d 以内，逐条删除，不得整份重写："+
			"先删已被推翻的假设，再删已落盘、只需留指针的内容，最后才合并「已确立事实」。"+
			"六个固定章节不得改名或新增；「已排除」中的既有条目必须逐字保留。压缩完再处理上面的请求。%s",
			casePath, tokens, HardTokenLimit, HardTokenLimit, formatIssues(issues))
	}
	if tokens > SoftTokenLimit {
		return fmt.Sprintf("[案卷] `%s` 约 %d tokens，已过软上限 %d。"+
			"本轮逐条删除过时内容，不得整份重写；只准使用固定六节，「已排除」既有条目不得删除、改写或并入其他章节。%s",
			casePath, tokens, SoftTokenLimit, formatIssues(issues))
	}
	return fmt.Sprintf("[案卷] 每轮结束前增量整理 `%s`：删掉被推翻假设和已完成待办；"+
		"只准使用固定六节，不新增日期/MR/阶段标题；「已排除」既有条目不得删除、改写或并入其他章节。%s",
		casePath, formatIssues(issues))
}

// SystemPrompt is the session-opening instruction block: where the case is,
// what each section is for, and what to do when it is over the cap.
//
// The size verdict is computed here rather than described in prose, so the
// agent is told "you are 1.3k over" instead of being asked to judge for itself
// whether it has written too much.
func SystemPrompt(casePath, doc string) string {
	tokens := EstimateTokens(doc)
	evidencePath, artifactsPath := supportPaths(casePath)
	var b strings.Builder
	b.WriteString("## 案卷模式\n\n")
	b.WriteString("这个话题的持久状态在案卷文件里，不在对话历史里。会话随时可能被轮换掉，")
	b.WriteString("**只有写进案卷的内容会活到下一轮**。\n\n")
	b.WriteString("案卷路径：`" + casePath + "`\n\n")
	b.WriteString("规则：\n")
	b.WriteString("- 每轮结束前增量整理案卷：更新结论，同时删除被推翻假设、已完成待办和已由产物指针替代的正文；不要等到超限才整理。\n")
	b.WriteString("- 二级标题是封闭集合且顺序固定：目标、已确立事实、已排除、当前假设、未闭环、产物坐标。禁止改名、合并或新增日期/MR/阶段标题；日期和 MR 写在条目中。\n")
	b.WriteString("- 每条断言必须带证据坐标（文件:行 / URL / SHA / 查询语句），这样才能写得短，也才能被复核。\n")
	b.WriteString("- **假设被推翻就整条删掉**，不要保留「我曾经以为」。案卷是可以删的，这是它比对话强的地方。\n")
	b.WriteString("- 「已排除」是防止绕圈的唯一手段：既有条目禁止删除、改写、改名或并入「历史修复」等章节；只有人工编辑可以纠正它。\n")
	b.WriteString("- 只放指针不放内容：大段日志、完整 diff、原始 JSON 一律落盘，案卷里只留路径。\n")
	b.WriteString("- 支撑材料必须进入本案卷专属目录：`" + evidencePath + "/` 放需要长期复核的输入和审核记录，`" + artifactsPath + "/` 放可重建、结案后可清理的生成物；禁止写入无案卷名的公共目录。角色权限仍然优先：只审不做的 agent 只能产出审核记录，不能为了取证执行修改、发布或部署。\n")
	b.WriteString("- 同一话题的其他 agent 读写同一份案卷，交接靠它，不靠复述对话。\n\n")
	if issues := documentIssues(doc); len(issues) > 0 {
		b.WriteString("**当前案卷结构不合规，本轮先按语义迁回固定六节；不得丢失任何「已排除」条目：** ")
		b.WriteString(strings.Join(issues, "；"))
		b.WriteString("。\n\n")
	}
	fmt.Fprintf(&b, "当前案卷约 %d tokens，软上限 %d，硬上限 %d。\n", tokens, SoftTokenLimit, HardTokenLimit)
	switch {
	case tokens > HardTokenLimit:
		fmt.Fprintf(&b, "\n**案卷超出硬上限 %d tokens。本轮第一件事是把它压到 %d 以内**，"+
			"靠删除而不是靠改写：先删已被推翻的假设，再删已经落盘、案卷里只需要留指针的内容，"+
			"最后才考虑合并「已确立事实」。「已排除」保留。压缩完再处理用户的请求。\n",
			tokens-HardTokenLimit, HardTokenLimit)
	case tokens > SoftTokenLimit:
		b.WriteString("\n已经超过软上限，本轮更新案卷时顺手删掉过时的行，不要只增不减。\n")
	}
	b.WriteString("\n当前案卷内容：\n\n<case>\n")
	b.WriteString(doc)
	b.WriteString("\n</case>\n")
	return b.String()
}

func supportPaths(casePath string) (string, string) {
	slashed := filepath.ToSlash(casePath)
	dir := path.Dir(slashed)
	name := strings.TrimSuffix(path.Base(slashed), path.Ext(slashed))
	return path.Join(dir, "evidence", name), path.Join(dir, "artifacts", name)
}

func formatIssues(issues []string) string {
	if len(issues) == 0 {
		return ""
	}
	return " 当前结构不合规，本轮先修复：" + strings.Join(issues, "；") + "。"
}
