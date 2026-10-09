package codexapp

import (
	"strings"
	"testing"
)

// Operator-facing failures name the transport as "CAS", never by the verbose
// "app-server" spelling that only belongs in argv and config values.
func TestErrorsUseCASAbbreviation(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"server reclaimed", errServerReclaimed},
		{"backlog overflow", errEventBacklogOverflow},
		{"pool closed", errPoolClosed},
		{"rpc error", &rpcError{Code: -32601, Message: "nope"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.Error()
			if strings.Contains(msg, "app-server") {
				t.Errorf("error still spells out app-server: %q", msg)
			}
			if !strings.Contains(msg, "CAS") {
				t.Errorf("error does not name CAS: %q", msg)
			}
		})
	}
}
