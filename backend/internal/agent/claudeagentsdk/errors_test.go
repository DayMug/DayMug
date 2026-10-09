package claudeagentsdk

import (
	"strings"
	"testing"
)

// Operator-facing failures name the transport as "CAS" rather than spelling
// out "Claude Agent SDK".
func TestErrorsUseCASAbbreviation(t *testing.T) {
	for _, err := range []error{errBridgeRetired, errPoolClosed} {
		msg := err.Error()
		if strings.Contains(strings.ToLower(msg), "agent sdk") {
			t.Errorf("error still spells out Agent SDK: %q", msg)
		}
		if !strings.Contains(msg, "CAS") {
			t.Errorf("error does not name CAS: %q", msg)
		}
	}
}

// The bridge is a Node script, so its stderr prefix is the one CAS string no
// Go-side rename can reach. It is what users actually see on a failed turn
// ("CAS bridge: API Error: 529 Overloaded"), so guard it here.
func TestBridgeStderrPrefixUsesCAS(t *testing.T) {
	src := string(bridgeSource)
	if !strings.Contains(src, "`CAS bridge: ") {
		t.Error("bridge.mjs no longer prefixes failures with \"CAS bridge: \"")
	}
	if strings.Contains(src, "claude-agent-sdk bridge:") {
		t.Error("bridge.mjs still spells out claude-agent-sdk in its failure prefix")
	}
}
