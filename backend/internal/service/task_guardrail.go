package service

import (
	"fmt"
	"strings"
	"sync"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
)

// GuardrailSnapshot describes the previous user-triggered task that crossed a
// configured usage threshold. It is deliberately a reminder, not an active
// question: the next user message consumes it and continues normally.
type GuardrailSnapshot struct {
	Prompt  string
	Trigger string
	// Triggers holds the GuardrailTrigger* codes behind Trigger.
	Triggers   []string
	ModelCalls int
	ToolCalls  int
	CostUSD    float64
	// RecordedCostUSD is the conversation's cost including this task; zero
	// while the task has not reported its cost.
	RecordedCostUSD float64
}

func (s GuardrailSnapshot) notice() *Notice {
	return &Notice{
		Kind:            NoticeKindTaskGuardrail,
		Triggers:        s.Triggers,
		ModelCalls:      s.ModelCalls,
		ToolCalls:       s.ToolCalls,
		RecordedCostUSD: s.RecordedCostUSD,
	}
}

var guardrailTriggerLabels = map[string]string{
	GuardrailTriggerModelCalls: "模型请求次数",
	GuardrailTriggerToolCalls:  "工具调用次数",
	GuardrailTriggerCost:       "预估成本",
}

// GuardrailReminderTracker carries a completed turn's reminder to the next
// user message in the same conversation. Runtime owns one tracker so Web and
// IM turns share the same boundary while unrelated conversations stay isolated.
type GuardrailReminderTracker struct {
	mu      sync.Mutex
	pending map[string]GuardrailSnapshot
}

func NewGuardrailReminderTracker() *GuardrailReminderTracker {
	return &GuardrailReminderTracker{pending: make(map[string]GuardrailSnapshot)}
}

func (t *GuardrailReminderTracker) Put(conversationID string, snapshot *GuardrailSnapshot) {
	if t == nil || conversationID == "" || snapshot == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending[conversationID] = *snapshot
}

func (t *GuardrailReminderTracker) Take(conversationID string) (GuardrailSnapshot, bool) {
	if t == nil || conversationID == "" {
		return GuardrailSnapshot{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	snapshot, ok := t.pending[conversationID]
	delete(t.pending, conversationID)
	return snapshot, ok
}

type taskGuardrail struct {
	cfg config.GuardrailsConfig

	modelCalls            int
	toolCalls             int
	costUSD               float64
	conversationCostUSD   float64
	conversationCostKnown bool
	explicitCalls         bool
}

func newTaskGuardrail(cfg config.GuardrailsConfig) *taskGuardrail {
	return &taskGuardrail{cfg: cfg}
}

func (g *taskGuardrail) enabled() bool {
	return g != nil && (g.cfg.MaxModelCallsPerTurn > 0 || g.cfg.MaxToolCallsPerTurn > 0 || g.cfg.MaxCostUSDPerTurn > 0)
}

// observe records usage without interrupting the active task. Claude exposes
// model starts directly; Codex only exposes a per-request usage notification,
// so that notification remains its closest observable model-call boundary.
// Sub-agent calls stay outside the parent task's totals.
func (g *taskGuardrail) observe(evt agent.StreamEvent) {
	if !g.enabled() {
		return
	}
	if evt.Subagent {
		if evt.Kind == agent.KindModelCall || evt.ModelCall {
			// Proof that this backend emits explicit model-call frames means its
			// usage reports must not be counted as requests a second time.
			g.explicitCalls = true
		}
		return
	}
	if evt.Kind == agent.KindModelCall || evt.ModelCall {
		g.explicitCalls = true
		g.modelCalls++
	} else if evt.Kind == agent.KindUsage && !g.explicitCalls {
		g.modelCalls += usageModelCalls(evt)
	}
	if evt.Kind == agent.KindToolUseStart {
		g.toolCalls++
	}
}

// addCost charges what the usage report actually billed. For providers that
// report a session-running total, MessagePersister has already reduced it to
// this turn's increment.
func (g *taskGuardrail) addCost(usd float64) {
	if g == nil || usd <= 0 {
		return
	}
	g.costUSD += usd
}

func (g *taskGuardrail) setConversationCost(usd float64) {
	if g == nil || usd < 0 {
		return
	}
	g.conversationCostUSD = usd
	g.conversationCostKnown = true
}

func usageModelCalls(evt agent.StreamEvent) int {
	if usage, ok := agent.UsageOf(evt); ok && usage.NumTurns > 0 {
		return int(usage.NumTurns)
	}
	return 1
}

func (g *taskGuardrail) reached() []string {
	var hit []string
	if g.cfg.MaxModelCallsPerTurn > 0 && g.modelCalls >= g.cfg.MaxModelCallsPerTurn {
		hit = append(hit, GuardrailTriggerModelCalls)
	}
	if g.cfg.MaxToolCallsPerTurn > 0 && g.toolCalls >= g.cfg.MaxToolCallsPerTurn {
		hit = append(hit, GuardrailTriggerToolCalls)
	}
	if g.cfg.MaxCostUSDPerTurn > 0 && g.costUSD >= g.cfg.MaxCostUSDPerTurn {
		hit = append(hit, GuardrailTriggerCost)
	}
	return hit
}

// reminder is called only after the active run has finished. The returned
// snapshot is held until the next user message, so threshold checks can never
// turn into an in-flight question or cancellation.
func (g *taskGuardrail) reminder() *GuardrailSnapshot {
	if g == nil {
		return nil
	}
	hit := g.reached()
	if len(hit) == 0 {
		return nil
	}
	labels := make([]string, len(hit))
	for i, code := range hit {
		labels[i] = guardrailTriggerLabels[code]
	}
	trigger := strings.Join(labels, "、")
	costSummary := "该任务尚未返回完整成本用量。"
	recordedCost := g.costUSD
	if g.conversationCostKnown {
		recordedCost += g.conversationCostUSD
	}
	if recordedCost > 0 {
		costSummary = fmt.Sprintf("本会话已记录成本 $%.4f。", recordedCost)
	}
	prompt := fmt.Sprintf(
		"上一条消息触发的任务已达到%s提醒：模型请求 %d 次，工具调用 %d 次。%s本条消息仍会正常执行；如需降低后续上下文成本，建议新建对话。",
		trigger, g.modelCalls, g.toolCalls, costSummary,
	)
	return &GuardrailSnapshot{
		Prompt: prompt, Trigger: trigger, Triggers: hit, ModelCalls: g.modelCalls,
		ToolCalls: g.toolCalls, CostUSD: g.costUSD, RecordedCostUSD: recordedCost,
	}
}
