package imbridge

import (
	"context"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/service"

	"github.com/DayMug/DayMug/backend/internal/imbot/imbottest"
)

func TestIMBridgeQueueUpdatesSharedBySlackAndFeishu(t *testing.T) {
	for _, platform := range []string{"slack", "feishu"} {
		t.Run(platform, func(t *testing.T) {
			cfg := &config.Config{Providers: []config.Provider{{
				Name: "acc1", Type: config.CLITypeClaude, MaxConcurrent: 1,
			}}}
			pool := service.NewPool(cfg)
			holder, err := pool.EnterForUser("", "acc1", "")
			if err != nil || holder.Wait(context.Background()) != nil {
				t.Fatalf("acquire holder: %v", err)
			}
			aheadTicket, err := pool.EnterForUser("", "acc1", "")
			if err != nil {
				t.Fatal(err)
			}
			target, err := pool.EnterForUser("", "acc1", "")
			if err != nil {
				t.Fatal(err)
			}

			recorder := &imbottest.ReplyRecorder{}
			bridge := &IMBridge{Runtime: &service.Runtime{Broadcaster: service.NewBroadcaster()}}
			done := make(chan struct{})
			go func() {
				defer close(done)
				bridge.notifyIMQueuePositions(target, "conversation", imbot.Message{
					Platform: platform,
				}, recorder)
			}()

			imbottest.WaitForReplyContaining(t, recorder, "前面共有 2 个任务，其中 1 个正在执行")
			holder.Release()
			if err := aheadTicket.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			imbottest.WaitForReplyContaining(t, recorder, "前面共有 1 个任务，其中 1 个正在执行")
			aheadTicket.Release()
			if err := target.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			target.Release()

			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("queue notifier did not stop after the slot was granted")
			}
		})
	}
}
