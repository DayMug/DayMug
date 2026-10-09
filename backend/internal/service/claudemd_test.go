package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSyncClaudeMd(t *testing.T) {
	dir := t.TempDir()
	content := "# Test CLAUDE.md\nSome rules here."

	if err := SyncClaudeMd(dir, content); err != nil {
		t.Fatalf("SyncClaudeMd: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != content {
		t.Errorf("expected %q, got %q", content, string(data))
	}
}

func TestSyncClaudeMd_EmptyWorkDir(t *testing.T) {
	err := SyncClaudeMd("", "content")
	if err == nil {
		t.Error("expected error for empty work_dir")
	}
}

func TestReadClaudeMd(t *testing.T) {
	dir := t.TempDir()
	content := "# Existing CLAUDE.md"
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	result, err := ReadClaudeMd(dir)
	if err != nil {
		t.Fatalf("ReadClaudeMd: %v", err)
	}
	if result != content {
		t.Errorf("expected %q, got %q", content, result)
	}
}

func TestReadClaudeMd_NotExists(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadClaudeMd(dir)
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestReadClaudeMd_EmptyWorkDir(t *testing.T) {
	_, err := ReadClaudeMd("")
	if err == nil {
		t.Error("expected error for empty work_dir")
	}
}
