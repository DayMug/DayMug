package streamcommon

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

func TestEmitterDeliversInOrder(t *testing.T) {
	ch := make(chan agent.StreamEvent, 3)
	em := NewEmitter(context.Background(), ch, "test")

	err := em.EmitAll([]agent.StreamEvent{
		{Kind: agent.KindDelta, Content: "a"},
		{Kind: agent.KindDelta, Content: "b"},
		{Kind: agent.KindResult, Content: "ab"},
	})
	if err != nil {
		t.Fatalf("EmitAll: %v", err)
	}
	close(ch)

	var got string
	for evt := range ch {
		got += evt.Content
	}
	if got != "abab" {
		t.Fatalf("got %q, want %q", got, "abab")
	}
}

func TestEmitterStallsOnAbandonedConsumer(t *testing.T) {
	// Unbuffered and never read: the shape of a consumer that deadlocked
	// inside its own persistence path.
	ch := make(chan agent.StreamEvent)
	em := NewEmitter(context.Background(), ch, "test").WithTimeout(20 * time.Millisecond)

	err := em.Emit(agent.StreamEvent{Kind: agent.KindToolResult})
	if !errors.Is(err, ErrConsumerStalled) {
		t.Fatalf("first Emit = %v, want ErrConsumerStalled", err)
	}
	if !em.Stalled() {
		t.Fatal("Stalled() = false after a stall")
	}

	// The error is sticky, and the next send must not pay the timeout again:
	// an adapter that keeps streaming past the failure would otherwise add
	// 20ms (30s in production) per remaining frame.
	start := time.Now()
	if err := em.Emit(agent.StreamEvent{Kind: agent.KindDelta}); !errors.Is(err, ErrConsumerStalled) {
		t.Fatalf("second Emit = %v, want ErrConsumerStalled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Millisecond {
		t.Fatalf("second Emit blocked for %s; the sticky error should return immediately", elapsed)
	}
}

func TestEmitterReportsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	em := NewEmitter(ctx, make(chan agent.StreamEvent), "test").WithTimeout(time.Minute)
	err := em.Emit(agent.StreamEvent{Kind: agent.KindDelta})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Emit = %v, want context.Canceled", err)
	}
	// A cancelled turn is the user hitting stop, not a broken consumer; the
	// two are reported to the caller differently on purpose.
	if em.Stalled() {
		t.Fatal("Stalled() = true for a cancelled context")
	}
	if errors.Is(err, ErrConsumerStalled) {
		t.Fatal("cancellation must not be reported as a consumer stall")
	}
}

func TestEmitterForwardsMalformedEventsAndWarnsOnce(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	ch := make(chan agent.StreamEvent, 3)
	em := NewEmitter(context.Background(), ch, "test")

	broken := agent.StreamEvent{Kind: agent.KindToolResult, Content: `{"id":"t1","path":"C:\work"}`}
	for range 2 {
		if err := em.Emit(broken); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}

	// Forwarded, not dropped: a malformed frame renders badly, a missing one
	// leaves a hole in the transcript with nothing to explain it.
	if len(ch) != 2 {
		t.Fatalf("delivered %d events, want both forwarded", len(ch))
	}
	if n := strings.Count(logged.String(), "emitting a malformed event"); n != 1 {
		t.Fatalf("logged %d warnings, want exactly 1 per turn", n)
	}
	if !strings.Contains(logged.String(), "not valid JSON") {
		t.Fatalf("warning does not say what was wrong: %q", logged.String())
	}
}

func TestEmitterIgnoresNonPositiveTimeout(t *testing.T) {
	em := NewEmitter(context.Background(), make(chan agent.StreamEvent, 1), "test").WithTimeout(0)
	if em.timeout != DefaultSendTimeout {
		t.Fatalf("timeout = %s, want the default %s", em.timeout, DefaultSendTimeout)
	}
}
