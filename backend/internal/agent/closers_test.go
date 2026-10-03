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

func TestStopResidentAsksEveryStopper(t *testing.T) {
	residentStoppers.mu.Lock()
	saved := residentStoppers.stoppers
	residentStoppers.stoppers = nil
	residentStoppers.mu.Unlock()
	t.Cleanup(func() {
		residentStoppers.mu.Lock()
		residentStoppers.stoppers = saved
		residentStoppers.mu.Unlock()
	})

	var asked []string
	RegisterResidentStopper(func(id string) bool { asked = append(asked, "a:"+id); return false })
	RegisterResidentStopper(nil)
	RegisterResidentStopper(func(id string) bool { asked = append(asked, "b:"+id); return id == "conv" })

	if !StopResident("conv") {
		t.Fatal("StopResident = false, want true when one adapter held the conversation")
	}
	if StopResident("other") {
		t.Fatal("StopResident = true for a conversation nobody holds")
	}
	if StopResident("") {
		t.Fatal("StopResident = true for an empty id")
	}
	if len(asked) != 4 || asked[0] != "a:conv" || asked[1] != "b:conv" {
		t.Fatalf("stoppers asked = %v", asked)
	}
}
