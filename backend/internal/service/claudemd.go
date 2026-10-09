package service

import (
	"fmt"
	"os"
	"path/filepath"
)

// SyncClaudeMd writes content to {workDir}/CLAUDE.md.
func SyncClaudeMd(workDir, content string) error {
	if workDir == "" {
		return fmt.Errorf("work_dir is empty")
	}
	path := filepath.Join(workDir, "CLAUDE.md")
	return os.WriteFile(path, []byte(content), 0o644)
}

// ReadClaudeMd reads the existing CLAUDE.md from workDir.
func ReadClaudeMd(workDir string) (string, error) {
	if workDir == "" {
		return "", fmt.Errorf("work_dir is empty")
	}
	path := filepath.Join(workDir, "CLAUDE.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
