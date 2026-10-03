package casefile

import (
	"os"
	"strconv"
	"strings"
)

// File is one agent's on-disk copy of a thread's case, plus the marker files
// kept beside it.
//
// The case is exchanged through a file rather than a bespoke tool call for
// three reasons: the agent needs no new capability to read or edit it, a human
// can open and correct it directly (the strongest steering available — a human
// edit deletes a wrong line, where a chat correction only appends a right one),
// and it is an ordinary file a human can diff, copy, or hand to another tool.
//
// The store copy is authoritative, not the file: two agents on one thread have
// two scopes, and only the store sees both. The markers exist so the next turn
// can tell apart the three reasons a local file can differ from the store — a
// human edit, a stale copy, and an agent write that never reached the store.
//
// File does no containment checking: Path must already be a path the caller
// is allowed to write.
type File struct {
	Path string
}

// Write replaces the case file's contents.
func (f File) Write(doc string) error {
	return os.WriteFile(f.Path, []byte(doc), 0o644)
}

// syncPath records which stored revision this copy was last synced from. It
// is what lets AdoptLocalEdit tell a human's edit apart from a stale copy.
func (f File) syncPath() string { return f.Path + ".version" }

// rejectionPath holds the explanation for an update the platform refused, so
// the next turn can be told about it.
//
// The notice has to outlive the run that produced it: the read-back executes
// after the agent has already answered, and the turn that needs to hear about
// it is the next one — often a different session. Silence was the real defect
// here: the agent saw its edit reappear reverted with no reason given, so its
// most likely next move was to write the same rejected text again.
func (f File) rejectionPath() string { return f.Path + ".rejected" }

// UnsavedPath marks that this copy holds a turn's edit that never reached the
// store.
//
// Without it the recovery path lies about who wrote the case: a failed save
// leaves the file ahead of the sync marker, which is the same shape as a human
// editing the file between turns, so the agent's own text is re-adopted as
// "human". Production shows how thoroughly that drowns the real signal — 43 of
// 47 "human" revisions landed within an hour of a failed save — and a human
// edit is the strongest correction this design has, so it has to stay
// distinguishable.
func (f File) UnsavedPath() string { return f.Path + ".unsaved" }

// MarkSynced records the stored revision the file now matches.
func (f File) MarkSynced(version int) error {
	return os.WriteFile(f.syncPath(), []byte(strconv.Itoa(version)), 0o644)
}

// SyncedVersion reads the revision this copy last wrote out, or 0.
func (f File) SyncedVersion() int {
	raw, err := os.ReadFile(f.syncPath())
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0
	}
	return n
}

// RecordRejection leaves the notice for the next turn.
func (f File) RecordRejection(reason string) error {
	return os.WriteFile(f.rejectionPath(), []byte(reason), 0o644)
}

// ConsumeRejection returns the pending rejection notice and clears it, so the
// agent is told once rather than on every later turn.
func (f File) ConsumeRejection() string {
	raw, err := os.ReadFile(f.rejectionPath())
	if err != nil {
		return ""
	}
	_ = os.Remove(f.rejectionPath())
	return strings.TrimSpace(string(raw))
}

// MarkUnsaved records whose text the file holds after a failed save, so the
// retry is not filed as a human edit.
func (f File) MarkUnsaved(author string) error {
	return os.WriteFile(f.UnsavedPath(), []byte(author), 0o644)
}

// ClearUnsaved drops the unsaved marker once the file's text reached the store.
func (f File) ClearUnsaved() { _ = os.Remove(f.UnsavedPath()) }

// HasUnsaved reports whether the last turn's save was dropped.
func (f File) HasUnsaved() bool {
	_, err := os.Stat(f.UnsavedPath())
	return err == nil
}

// AdoptLocalEdit decides whether the file already on disk should win over the
// stored revision (storedDoc at storedVersion; version 0 means nothing stored).
//
// This exists because a human editing the case file directly is the strongest
// correction available in this design — it deletes a wrong line, where a chat
// message can only append a right one — and blindly re-materializing the store
// copy every turn would silently discard exactly that.
//
// The rule: the local file wins only when this copy is already in sync with
// the stored revision. If the store has moved on (the other agent on the thread
// saved a newer revision), the store wins; the local file is stale, not edited.
// A tie between a human edit here and a peer's write elsewhere resolves to the
// peer, which is the safe direction — the human's version survives in the file
// they can still see, while a silently dropped peer revision would not.
func (f File) AdoptLocalEdit(storedDoc string, storedVersion int) (string, bool) {
	switch {
	case storedVersion == 0:
		// Nothing stored yet, so the only content that can be on disk is a
		// write that never landed — a first turn is materialized with the empty
		// template, never prose. Recovering it is not optional: the next turn
		// re-materializes that template over the file, so a dropped *first*
		// save loses the thread's case outright rather than one revision.
		if !f.HasUnsaved() {
			return "", false
		}
	case f.SyncedVersion() != storedVersion:
		return "", false
	}
	raw, err := os.ReadFile(f.Path)
	if err != nil {
		return "", false
	}
	local := string(raw)
	if local == storedDoc || strings.TrimSpace(local) == "" {
		return "", false
	}
	return local, true
}
