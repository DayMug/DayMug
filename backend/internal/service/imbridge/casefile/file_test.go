package casefile

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestFile(t *testing.T, doc string) File {
	t.Helper()
	f := File{Path: filepath.Join(t.TempDir(), "C1-T1.md")}
	if err := f.Write(doc); err != nil {
		t.Fatal(err)
	}
	return f
}

// A file that matches the revision it was synced from, but whose text differs
// from the store, is a human's edit and wins.
func TestAdoptLocalEditTakesAnEditMadeOnAnInSyncCopy(t *testing.T) {
	f := newTestFile(t, "edited by a human")
	if err := f.MarkSynced(3); err != nil {
		t.Fatal(err)
	}
	got, ok := f.AdoptLocalEdit("stored text", 3)
	if !ok || got != "edited by a human" {
		t.Fatalf("AdoptLocalEdit = %q, %v; want the local edit adopted", got, ok)
	}
}

// When the store has moved past the revision this copy was synced from, the
// local file is stale rather than edited, and the peer's revision must win.
func TestAdoptLocalEditRejectsAStaleCopy(t *testing.T) {
	f := newTestFile(t, "old local text")
	if err := f.MarkSynced(2); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.AdoptLocalEdit("peer's newer text", 3); ok {
		t.Fatal("a stale local copy overrode a newer stored revision")
	}
}

// Nothing stored yet means the only prose that can be on disk is a first save
// that never landed — adopted only when the unsaved marker says so.
func TestAdoptLocalEditOnAnEmptyStoreNeedsTheUnsavedMarker(t *testing.T) {
	f := newTestFile(t, "a first save that failed")
	if _, ok := f.AdoptLocalEdit("", 0); ok {
		t.Fatal("adopted a file with nothing stored and no unsaved marker")
	}
	if err := f.MarkUnsaved("Ada"); err != nil {
		t.Fatal(err)
	}
	if !f.HasUnsaved() {
		t.Fatal("HasUnsaved() = false right after MarkUnsaved")
	}
	if got, ok := f.AdoptLocalEdit("", 0); !ok || got != "a first save that failed" {
		t.Fatalf("AdoptLocalEdit = %q, %v; want the dropped first save recovered", got, ok)
	}
	f.ClearUnsaved()
	if f.HasUnsaved() {
		t.Fatal("ClearUnsaved left the marker behind")
	}
}

func TestAdoptLocalEditIgnoresAnUnchangedOrBlankFile(t *testing.T) {
	f := newTestFile(t, "same")
	if err := f.MarkSynced(1); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.AdoptLocalEdit("same", 1); ok {
		t.Fatal("an unchanged file was reported as an edit")
	}
	if err := f.Write("  \n"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.AdoptLocalEdit("same", 1); ok {
		t.Fatal("a blanked file was adopted over the stored case")
	}
}

// The rejection notice is delivered exactly once.
func TestRejectionNoticeIsConsumedOnce(t *testing.T) {
	f := newTestFile(t, "doc")
	if err := f.RecordRejection("  structure drift \n"); err != nil {
		t.Fatal(err)
	}
	if got := f.ConsumeRejection(); got != "structure drift" {
		t.Fatalf("ConsumeRejection() = %q, want the trimmed reason", got)
	}
	if got := f.ConsumeRejection(); got != "" {
		t.Fatalf("second ConsumeRejection() = %q, want it cleared", got)
	}
}

func TestSyncedVersionDefaultsToZero(t *testing.T) {
	f := newTestFile(t, "doc")
	if got := f.SyncedVersion(); got != 0 {
		t.Fatalf("SyncedVersion() with no marker = %d, want 0", got)
	}
	if err := os.WriteFile(f.Path+".version", []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := f.SyncedVersion(); got != 0 {
		t.Fatalf("SyncedVersion() with an unreadable marker = %d, want 0", got)
	}
}
