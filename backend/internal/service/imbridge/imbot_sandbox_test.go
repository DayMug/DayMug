package imbridge

import (
	"context"
	"testing"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"

	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// markerSandbox is compared by identity: these tests assert the bridge forwards
// *the server's* sandbox into every IM turn, not that wrapping works.
type markerSandbox struct{}

func (markerSandbox) Wrap(argv []string, _ string, _ agent.WrapOpts) ([]string, []string, error) {
	return argv, nil, nil
}

const (
	imStallTimeout     = 4 * time.Minute
	imMaxSilentTimeout = 45 * time.Minute
)

// newSandboxTestRun reuses the slot-test fixture and adds what routes.go wires
// in production: a sandbox and the operator's runner watchdogs.
func newSandboxTestRun(t *testing.T) *imRun {
	t.Helper()
	r, _, _ := newSlotTestRun(t, 1)
	r.bridge.Sandbox = markerSandbox{}
	r.bridge.Cfg.RunnerStallTimeout = config.Duration{Duration: imStallTimeout}
	r.bridge.Cfg.RunnerMaxSilentTimeout = config.Duration{Duration: imMaxSilentTimeout}
	return r
}

// Every Slack / 飞书 turn goes through buildRunRequest. It used to hand the
// runner a nil Sandbox, so `sandbox.enabled: true` jailed only the browser
// chat while IM conversations ran bare in the agent's real work dir.
func TestIMRunRequestIsSandboxed(t *testing.T) {
	r := newSandboxTestRun(t)

	opts, cleanup := r.buildRunRequest(context.Background())
	defer cleanup()

	if _, ok := opts.Sandbox.(markerSandbox); !ok {
		t.Errorf("Sandbox = %#v, want the server's sandbox", opts.Sandbox)
	}
	if opts.JailRoot != r.workDir() {
		t.Errorf("JailRoot = %q, want the turn's work dir %q", opts.JailRoot, r.workDir())
	}
	// The agent row is an orphan here (no resolvable owner), which must fail
	// safe towards more isolation rather than towards a free-roaming agent.
	if opts.Unrestricted {
		t.Error("an agent with no resolvable owner must stay jailed")
	}
}

func TestIMRunRequestCarriesWatchdogs(t *testing.T) {
	r := newSandboxTestRun(t)

	opts, cleanup := r.buildRunRequest(context.Background())
	defer cleanup()

	if opts.StallTimeout != imStallTimeout {
		t.Errorf("StallTimeout = %v, want %v", opts.StallTimeout, imStallTimeout)
	}
	if opts.MaxSilentTimeout != imMaxSilentTimeout {
		t.Errorf("MaxSilentTimeout = %v, want %v", opts.MaxSilentTimeout, imMaxSilentTimeout)
	}
}

// An admin owner lifts the jail for their agents, matching the chat path — an
// IM turn must not be stricter than the same person's browser turn.
func TestIMRunRequestInheritsAdminOwnerTier(t *testing.T) {
	r := newSandboxTestRun(t)
	fake, ok := r.bridge.Store.(*storetest.Fake)
	if !ok {
		t.Fatalf("unexpected store type %T", r.bridge.Store)
	}
	fake.Users = append(fake.Users, store.User{
		ID: "owner1", Username: "root", Email: "root@example.com", IsAdmin: true,
	})
	r.agentUser.Email = "root@example.com"

	opts, cleanup := r.buildRunRequest(context.Background())
	defer cleanup()

	if !opts.Unrestricted {
		t.Error("an admin owner's agent should bypass the jail, like the chat path")
	}
}
