package codexcli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestSeedMcpServers_MergesDayMugConfigIntoCodexConfig(t *testing.T) {
	dir := t.TempDir()
	existing := "[mcp_servers.existing]\ncommand = \"existing\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(existing), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	mcpPath := filepath.Join(t.TempDir(), "mcp.json")
	mcpJSON := `{"mcpServers":{"knowledge":{"command":"knowledge-mcp","args":["serve"]},"search":{"command":"search-mcp","args":["serve"],"env":{"SEARCH_CACHE":"1"}}}}`
	if err := os.WriteFile(mcpPath, []byte(mcpJSON), 0o600); err != nil {
		t.Fatalf("write mcp: %v", err)
	}

	seedMcpServers(dir, mcpPath)

	raw, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg map[string]any
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config.toml not valid TOML: %v\n%s", err, raw)
	}
	servers, _ := cfg["mcp_servers"].(map[string]any)
	if servers["existing"] == nil || servers["knowledge"] == nil || servers["search"] == nil {
		t.Fatalf("mcp servers not merged: %v", servers)
	}
}

func TestSeedMcpServers_AbortsOnBadTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "not = = valid"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	mcpPath := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{"knowledge":{"command":"knowledge-mcp"}}}`), 0o600); err != nil {
		t.Fatalf("write mcp: %v", err)
	}

	seedMcpServers(dir, mcpPath)

	raw, _ := os.ReadFile(path)
	if string(raw) != original {
		t.Fatalf("bad TOML was modified: %q", raw)
	}
}
