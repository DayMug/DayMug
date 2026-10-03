package agent

import (
	"context"
	"testing"
)

type steeringStub struct{ stubBackend }

func (steeringStub) SteerTurn(context.Context, string, string, string) error { return nil }

type stubBackend struct{ name string }

func (s stubBackend) Name() string                       { return s.name }
func (stubBackend) Capabilities() Capabilities           { return Capabilities{} }
func (stubBackend) SessionExists(_, _, _ string) bool    { return false }
func (stubBackend) SessionLogPath(_, _, _ string) string { return "" }
func (stubBackend) RunOneshot(context.Context, string, string, RunRequest) (string, error) {
	return "", nil
}
func (stubBackend) RunWithSession(_ context.Context, _, _ string, _ RunRequest, out chan<- StreamEvent) error {
	close(out)
	return nil
}

// Steering must follow the selected transport, not the union of the ones the
// switch could route to — otherwise the composer offers "insert" on a CLI
// transport that can only queue.
func TestTransportSwitchFollowsTheSelection(t *testing.T) {
	selected := "sdk"
	sw := NewTransportSwitch(func() string { return selected }, "sdk", map[string]Backend{
		"sdk": steeringStub{stubBackend{"sdk"}},
		"cli": stubBackend{"cli"},
	})

	if sw.Name() != "sdk" || !OptionalCapabilitiesOf(sw).Steering {
		t.Fatalf("sdk selected: name=%q steering=%v", sw.Name(), OptionalCapabilitiesOf(sw).Steering)
	}
	if _, ok := SteererOf(sw); !ok {
		t.Fatal("SteererOf must unwrap the switch")
	}

	selected = "cli"
	if sw.Name() != "cli" || OptionalCapabilitiesOf(sw).Steering {
		t.Fatalf("cli selected: name=%q steering=%v", sw.Name(), OptionalCapabilitiesOf(sw).Steering)
	}
	if Resolve(sw).Name() != "cli" {
		t.Fatalf("Resolve = %q, want cli", Resolve(sw).Name())
	}

	selected = "bogus"
	if sw.Name() != "sdk" {
		t.Fatalf("unknown selection resolved to %q, want the fallback", sw.Name())
	}
}
