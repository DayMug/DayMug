package codexcli

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// seedMcpServers bridges DayMug's Claude-style MCP JSON into Codex's native
// config.toml. Codex does not accept a per-run --mcp-config flag, so without
// this merge ToolSearch cannot discover account/project MCP servers.
func seedMcpServers(configDir, mcpConfigPath string) {
	if configDir == "" || mcpConfigPath == "" {
		return
	}
	raw, err := os.ReadFile(mcpConfigPath)
	if err != nil {
		log.Printf("[codex] mcp seed: read %s: %v", mcpConfigPath, err)
		return
	}

	var doc struct {
		McpServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		log.Printf("[codex] mcp seed: %s is not valid JSON, skipping: %v", mcpConfigPath, err)
		return
	}
	if len(doc.McpServers) == 0 {
		return
	}

	path := filepath.Join(configDir, "config.toml")
	cfg := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(data, &cfg); err != nil {
			log.Printf("[codex] mcp seed: %s is not valid TOML, leaving untouched: %v", path, err)
			return
		}
	} else if !os.IsNotExist(err) {
		log.Printf("[codex] mcp seed: read %s: %v", path, err)
		return
	}

	servers, _ := cfg["mcp_servers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	for name, server := range doc.McpServers {
		if strings.TrimSpace(name) == "" {
			continue
		}
		servers[name] = server
	}
	if len(servers) == 0 {
		return
	}
	cfg["mcp_servers"] = servers

	out, err := toml.Marshal(cfg)
	if err != nil {
		log.Printf("[codex] mcp seed: marshal: %v", err)
		return
	}
	writeFileAtomic(path, out)
}

// writeFileAtomic writes data via a temp file + rename so a crash mid-write
// can't truncate the live config (which sits next to the account's auth.json).
// 0o600 because the config tree may hold credentials. Errors are logged, not
// returned — every caller is best-effort.
func writeFileAtomic(path string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("[codex] config seed: mkdir %s: %v", filepath.Dir(path), err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("[codex] config seed: write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("[codex] config seed: rename %s: %v", path, err)
		_ = os.Remove(tmp)
	}
}
