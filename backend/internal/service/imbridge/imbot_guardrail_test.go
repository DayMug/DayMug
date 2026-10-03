package imbridge

import (
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/imbot/imbottest"
	"github.com/DayMug/DayMug/backend/internal/service"
)

func TestIMGuardrailReminderShowsUsageWithoutAskingForAChoice(t *testing.T) {
	recorder := &imbottest.ReplyRecorder{}
	progress := &imProgress{
		bridge: &IMBridge{Runtime: &service.Runtime{}},
		msg: imbot.Message{
			Platform: "feishu", ChannelID: "chat", ThreadID: "thread",
		},
		responder: recorder,
	}
	progress.guardrail(service.GuardrailSnapshot{
		Prompt: "上一条消息触发的任务模型请求 50 次，工具调用 100 次；本条消息仍会正常执行。",
	})
	replies := recorder.All()
	if len(replies) != 1 ||
		!strings.Contains(replies[0], "模型请求 50 次") ||
		strings.Contains(replies[0], "回复") ||
		strings.Contains(replies[0], "继续") ||
		strings.Contains(replies[0], "停止") {
		t.Fatalf("IM guardrail reminder = %v", replies)
	}
}
