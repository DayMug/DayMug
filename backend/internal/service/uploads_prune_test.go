package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testUploadsRetention = 90 * 24 * time.Hour

func writeUploadWithAge(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("chtimes %s: %v", name, err)
	}
	return path
}

func TestPruneUploadsRemovesOnlyExpiredFiles(t *testing.T) {
	dir := t.TempDir()
	stale := writeUploadWithAge(t, dir, "stale.png", 100*24*time.Hour)
	fresh := writeUploadWithAge(t, dir, "fresh.png", time.Hour)

	removed, err := PruneUploads(dir, testUploadsRetention, time.Now())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want only the expired file", removed)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("expired attachment survived the sweep")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("recent attachment was deleted: %v", err)
	}
}

// A file exactly at the cutoff is kept: the boundary must not delete something
// a transcript could still be rendering.
func TestPruneUploadsKeepsFileExactlyAtCutoff(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	path := writeUploadWithAge(t, dir, "edge.png", testUploadsRetention)

	if _, err := PruneUploads(dir, testUploadsRetention, now); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file exactly at the retention boundary was deleted: %v", err)
	}
}

// Sub-directories and symlinks are skipped — unlinking a stale symlink would
// let it decide the fate of whatever it points at.
func TestPruneUploadsSkipsNonRegularEntries(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "external.png")
	if err := os.WriteFile(outside, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	stamp := time.Now().Add(-200 * 24 * time.Hour)
	if err := os.Chtimes(link, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(nested, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	removed, err := PruneUploads(dir, testUploadsRetention, time.Now())
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want symlinks and directories left alone", removed)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Error("stale symlink was unlinked")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("symlink target was deleted")
	}
}

func TestPruneUploadsIgnoresNonPositiveTTL(t *testing.T) {
	dir := t.TempDir()
	path := writeUploadWithAge(t, dir, "ancient.png", 10000*time.Hour)

	removed, err := PruneUploads(dir, 0, time.Now())
	if err != nil || removed != 0 {
		t.Fatalf("removed = %d, err = %v; a non-positive TTL must disable the sweep", removed, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error("sweep ran despite a disabled TTL")
	}
}

// The sweep is triggered on every write, so it has to be rate-limited or a
// ten-attachment message would re-scan the directory ten times.
func TestClaimUploadsPruneThrottlesPerDirectory(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	if !claimUploadsPrune(dir, now) {
		t.Fatal("first sweep was refused")
	}
	if claimUploadsPrune(dir, now.Add(uploadsPruneInterval/2)) {
		t.Error("a second sweep inside the interval was allowed")
	}
	if !claimUploadsPrune(dir, now.Add(uploadsPruneInterval+time.Second)) {
		t.Error("sweep was still refused after the interval elapsed")
	}
	if !claimUploadsPrune(t.TempDir(), now) {
		t.Error("a different directory was throttled by an unrelated one's sweep")
	}
}

// Uploads are kept forever unless an operator sets retention.uploads.
func TestMaybePruneUploadsIsOffByDefault(t *testing.T) {
	dir := t.TempDir()
	path := writeUploadWithAge(t, dir, "ancient.png", 10000*time.Hour)

	MaybePruneUploads(dir)

	if _, err := os.Stat(path); err != nil {
		t.Errorf("upload deleted without a configured retention: %v", err)
	}
}
