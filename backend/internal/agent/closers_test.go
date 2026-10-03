package agent

import "testing"

func TestCloseRegisteredRunsEachCloserOnce(t *testing.T) {
	CloseRegistered() // drop anything an earlier test left behind

	calls := 0
	RegisterCloser(func() { calls++ })
	RegisterCloser(nil) // ignored rather than a nil-call panic at shutdown
	RegisterCloser(func() { calls++ })

	if n := CloseRegistered(); n != 2 {
		t.Fatalf("CloseRegistered = %d, want 2", n)
	}
	if calls != 2 {
		t.Fatalf("closers ran %d times, want 2", calls)
	}
	// The shutdown path can reach this twice (clean drain, then force-stop);
	// the second pass must be inert.
	if n := CloseRegistered(); n != 0 || calls != 2 {
		t.Fatalf("second CloseRegistered ran %d closer(s), total calls %d", n, calls)
	}
}
