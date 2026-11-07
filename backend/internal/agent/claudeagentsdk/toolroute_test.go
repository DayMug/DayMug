package claudeagentsdk

import (
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// The route's MCP server wins a name clash with the user's own config, so a
// user entry cannot stand in for the device.
func TestApplyToolRouteMergesAndOverrides(t *testing.T) {
	req := bridgeRequest{MCPServers: map[string]any{"mine": "keep", "daymug_local": "spoof"}}
	applyToolRoute(&req, &agent.ToolRoute{
		MCPServers: map[string]any{"daymug_local": "device"},
		Disallowed: []string{"Bash", "Read"},
		Aliases:    map[string]string{"Bash": "mcp__daymug_local__Bash"},
	})
	if req.MCPServers["mine"] != "keep" || req.MCPServers["daymug_local"] != "device" {
		t.Fatalf("mcp servers = %v", req.MCPServers)
	}
	if len(req.DisallowedTools) != 2 || req.ToolAliases["Bash"] != "mcp__daymug_local__Bash" {
		t.Fatalf("route = %v %v", req.DisallowedTools, req.ToolAliases)
	}

	plain := bridgeRequest{}
	applyToolRoute(&plain, nil)
	if plain.MCPServers != nil || plain.DisallowedTools != nil || plain.ToolAliases != nil {
		t.Fatalf("nil route changed the request: %+v", plain)
	}
}

// A routed request must not share a parked process with an unrouted one.
func TestBridgeFingerprintCoversToolRoute(t *testing.T) {
	base := bridgeRequest{CWD: "/w"}
	routed := base
	applyToolRoute(&routed, &agent.ToolRoute{Disallowed: []string{"Bash"}, Aliases: map[string]string{"Bash": "x"}})
	if bridgeFingerprint(agent.RunRequest{}, base) == bridgeFingerprint(agent.RunRequest{}, routed) {
		t.Fatal("tool route not part of the fingerprint")
	}
}
