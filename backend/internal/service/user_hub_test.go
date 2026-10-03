package service

import (
	"testing"
)

// drainHubChannel reads everything currently buffered in ch into a slice without
// blocking. Used to assert what each subscriber actually received.
func drainHubChannel(ch chan []byte) [][]byte {
	out := [][]byte{}
	for {
		select {
		case data := <-ch:
			out = append(out, data)
		default:
			return out
		}
	}
}

func TestUserHubBroadcastDeliversToAllSameUserClients(t *testing.T) {
	hub := NewUserHub()
	a := make(chan []byte, 4)
	b := make(chan []byte, 4)
	hub.Join("user-1", "client-a", a)
	hub.Join("user-1", "client-b", b)

	hub.Broadcast("user-1", []byte("hello"))

	if got := drainHubChannel(a); len(got) != 1 || string(got[0]) != "hello" {
		t.Errorf("client-a got %v, want one [\"hello\"]", got)
	}
	if got := drainHubChannel(b); len(got) != 1 || string(got[0]) != "hello" {
		t.Errorf("client-b got %v, want one [\"hello\"]", got)
	}
}

func TestUserHubBroadcastIsolatesUsers(t *testing.T) {
	hub := NewUserHub()
	mine := make(chan []byte, 4)
	theirs := make(chan []byte, 4)
	hub.Join("u-mine", "c1", mine)
	hub.Join("u-theirs", "c2", theirs)

	hub.Broadcast("u-mine", []byte("for-me"))

	if got := drainHubChannel(mine); len(got) != 1 {
		t.Errorf("own room delivery missing: %v", got)
	}
	if got := drainHubChannel(theirs); len(got) != 0 {
		t.Errorf("other user must not receive event, got %v", got)
	}
}

func TestUserHubBroadcastAllDeliversAcrossUserRooms(t *testing.T) {
	hub := NewUserHub()
	first := make(chan []byte, 4)
	second := make(chan []byte, 4)
	hub.Join("user-1", "client-1", first)
	hub.Join("user-2", "client-2", second)

	hub.BroadcastAll([]byte("global"))

	if got := drainHubChannel(first); len(got) != 1 || string(got[0]) != "global" {
		t.Errorf("first user got %v, want one global event", got)
	}
	if got := drainHubChannel(second); len(got) != 1 || string(got[0]) != "global" {
		t.Errorf("second user got %v, want one global event", got)
	}
}

func TestUserHubBroadcastExceptSkipsSender(t *testing.T) {
	hub := NewUserHub()
	sender := make(chan []byte, 4)
	peer := make(chan []byte, 4)
	hub.Join("u1", "sender", sender)
	hub.Join("u1", "peer", peer)

	hub.BroadcastExcept("u1", "sender", []byte("evt"))

	if got := drainHubChannel(sender); len(got) != 0 {
		t.Errorf("sender should be skipped, got %v", got)
	}
	if got := drainHubChannel(peer); len(got) != 1 {
		t.Errorf("peer should receive, got %v", got)
	}
}

func TestUserHubLeaveRemovesClient(t *testing.T) {
	hub := NewUserHub()
	ch := make(chan []byte, 4)
	hub.Join("u1", "c1", ch)
	hub.Leave("u1", "c1")
	hub.Broadcast("u1", []byte("noop"))

	if got := drainHubChannel(ch); len(got) != 0 {
		t.Errorf("expected no delivery after Leave, got %v", got)
	}
}

func TestUserHubBroadcastNonBlockingWhenChannelFull(t *testing.T) {
	hub := NewUserHub()
	// Buffer of 1, pre-fill it so the next send would block if the
	// hub didn't use a non-blocking select. The Broadcast call below
	// must return immediately rather than wait for the buffer to drain.
	ch := make(chan []byte, 1)
	ch <- []byte("seed")
	hub.Join("u1", "c1", ch)

	// If the hub blocked on a full channel this call would never
	// return — the test would hang and trip the package-level test
	// timeout. The synchronous return itself is the assertion.
	hub.Broadcast("u1", []byte("dropped"))

	// Defensive check: the seed value must still be there (the dropped
	// frame should NOT have replaced it; non-blocking sends to a full
	// channel are dropped at the sender).
	got := drainHubChannel(ch)
	if len(got) != 1 || string(got[0]) != "seed" {
		t.Errorf("expected only the seed frame, got %v", got)
	}
}

func TestUserHubGuardsAgainstEmptyArgs(t *testing.T) {
	hub := NewUserHub()
	ch := make(chan []byte, 4)
	// All of these should be no-ops without panicking.
	hub.Join("", "c1", ch)
	hub.Join("u1", "", ch)
	hub.Leave("", "c1")
	hub.Broadcast("", []byte("x"))
	hub.Broadcast("u1", nil)
	hub.BroadcastExcept("", "c1", []byte("x"))

	if got := drainHubChannel(ch); len(got) != 0 {
		t.Errorf("expected zero deliveries, got %v", got)
	}
}

func TestUserHubBroadcastAfterLastClientLeaves(t *testing.T) {
	hub := NewUserHub()
	ch := make(chan []byte, 4)
	hub.Join("u1", "c1", ch)
	hub.Leave("u1", "c1")
	// Room should be cleaned up; this is a smoke test that the next
	// Broadcast doesn't panic on a missing room map entry.
	hub.Broadcast("u1", []byte("ignored"))
}

// The hub has no replay buffer, so a dropped frame is only recoverable if the
// client can tell it happened. Each client counts the frames addressed to it;
// a gap in that count is the cue to refetch the conversation list.
func TestUserHub_BroadcastStampsPerClientSeq(t *testing.T) {
	h := NewUserHub()
	a := make(chan []byte, 8)
	b := make(chan []byte, 8)
	h.Join("u1", "tab-a", a)
	h.Join("u1", "tab-b", b)

	h.Broadcast("u1", marshalFrame(t, ServerMessage{Type: "conversation_updated"}))
	h.Broadcast("u1", marshalFrame(t, ServerMessage{Type: "conversation_updated"}))

	for _, ch := range []chan []byte{a, b} {
		if got := seqOf(t, <-ch); got != 1 {
			t.Fatalf("first frame seq = %d, want 1", got)
		}
		if got := seqOf(t, <-ch); got != 2 {
			t.Fatalf("second frame seq = %d, want 2", got)
		}
	}
}

// A client that couldn't take a frame must see the count jump, otherwise the
// loss is invisible and its sidebar stays stale until an unrelated refresh.
func TestUserHub_SeqAdvancesThroughAnUndeliverableFrame(t *testing.T) {
	h := NewUserHub()
	// One slot: the first frame fills it, the second is dropped.
	ch := make(chan []byte, 1)
	h.Join("u1", "tab", ch)

	frame := marshalFrame(t, ServerMessage{Type: "conversation_updated"})
	h.Broadcast("u1", frame)
	h.Broadcast("u1", frame)

	if got := seqOf(t, <-ch); got != 1 {
		t.Fatalf("delivered seq = %d, want 1", got)
	}
	// The channel has drained; the next frame gets through and its seq
	// exposes the one that didn't.
	h.Broadcast("u1", frame)
	if got := seqOf(t, <-ch); got != 3 {
		t.Fatalf("post-drop seq = %d, want 3 (a gap the client can see)", got)
	}
}

// BroadcastExcept skips the originator on purpose. Advancing its counter
// anyway would hand it a phantom gap — and a pointless REST refetch — for
// every conversation event it triggered itself.
func TestUserHub_BroadcastExceptDoesNotBurnTheSkippedClientsSeq(t *testing.T) {
	h := NewUserHub()
	origin := make(chan []byte, 8)
	peer := make(chan []byte, 8)
	h.Join("u1", "origin", origin)
	h.Join("u1", "peer", peer)

	frame := marshalFrame(t, ServerMessage{Type: "conversation_added"})
	h.BroadcastExcept("u1", "origin", frame)
	h.Broadcast("u1", frame)

	if got := seqOf(t, <-peer); got != 1 {
		t.Fatalf("peer first seq = %d, want 1", got)
	}
	if got := seqOf(t, <-peer); got != 2 {
		t.Fatalf("peer second seq = %d, want 2", got)
	}
	// The originator only ever received the second frame, and for it that is
	// frame number one — no gap.
	if got := seqOf(t, <-origin); got != 1 {
		t.Fatalf("origin seq = %d, want 1 (the skip must not count)", got)
	}
}

// BroadcastAll crosses user rooms, so the same payload necessarily carries a
// different number for each recipient — the counter belongs to the socket,
// not to the event.
func TestUserHub_BroadcastAllCountsPerClient(t *testing.T) {
	h := NewUserHub()
	early := make(chan []byte, 8)
	late := make(chan []byte, 8)
	h.Join("u1", "early", early)

	frame := marshalFrame(t, ServerMessage{Type: "conversation_activity"})
	h.BroadcastAll(frame)
	h.Join("u2", "late", late)
	h.BroadcastAll(frame)

	if got := seqOf(t, <-early); got != 1 {
		t.Fatalf("early client first seq = %d, want 1", got)
	}
	if got := seqOf(t, <-early); got != 2 {
		t.Fatalf("early client second seq = %d, want 2", got)
	}
	if got := seqOf(t, <-late); got != 1 {
		t.Fatalf("late joiner seq = %d, want 1", got)
	}
}

// Re-joining with the same client id resets the count. The client reads a
// non-advancing sequence as a restart, so this must not look like a gap.
func TestUserHub_RejoinRestartsTheCount(t *testing.T) {
	h := NewUserHub()
	first := make(chan []byte, 8)
	h.Join("u1", "tab", first)

	frame := marshalFrame(t, ServerMessage{Type: "conversation_updated"})
	h.Broadcast("u1", frame)
	h.Broadcast("u1", frame)

	second := make(chan []byte, 8)
	h.Join("u1", "tab", second)
	h.Broadcast("u1", frame)

	if got := seqOf(t, <-second); got != 1 {
		t.Fatalf("post-rejoin seq = %d, want 1", got)
	}
}
