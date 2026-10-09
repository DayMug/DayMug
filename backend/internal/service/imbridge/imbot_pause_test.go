package imbridge

import (
	"context"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/service"
)

func pausableBridge(t *testing.T, paused bool) *IMBridge {
	t.Helper()
	cfg := &config.Config{Providers: []config.Provider{{
		Name: "acc1", Type: config.CLITypeClaude, MaxConcurrent: 1,
	}}}
	gate := service.NewPauseGate()
	gate.SetPaused(paused)
	return &IMBridge{Runtime: &service.Runtime{Cfg: cfg, Pool: service.NewPool(cfg), Pause: gate}}
}

// An IM turn has no durable pending queue, so the pause has to refuse it —
// and the refusal must reach the sender before the run touches the store,
// or a paused window leaves half-prepared conversations behind.
func TestIMBridgeRefusesWhilePaused(t *testing.T) {
	b := pausableBridge(t, true)

	_, err := b.run(context.Background(), imbot.Message{Platform: "slack"}, imbot.ChannelRule{}, nil)
	if err == nil {
		t.Fatal("paused bridge accepted a message")
	}
	if !strings.Contains(err.Error(), "暂停") {
		t.Errorf("error %q does not tell the sender the service is paused", err)
	}
}

// Guards the default: an unpaused gate must not change how a message is
// handled. The run still fails (this bridge has no store), but it has to fail
// past the gate rather than at it.
func TestIMBridgeUnpausedFallsThrough(t *testing.T) {
	b := pausableBridge(t, false)

	_, err := b.run(context.Background(), imbot.Message{Platform: "slack"}, imbot.ChannelRule{}, nil)
	if err != nil && strings.Contains(err.Error(), "暂停") {
		t.Errorf("unpaused bridge refused with the pause message: %v", err)
	}
}
