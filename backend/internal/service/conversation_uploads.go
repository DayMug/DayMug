package service

import (
	"context"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// RemoveConversationUploads deletes the files a deleted conversation's
// messages attached — web uploads and inbound IM attachments under the agent's
// `.daymug/agents/<id>/` scope — and reports how many it removed.
//
// It runs after the soft delete, for every way a conversation goes away
// (manual delete, stale cleanup, the inactive-retention sweep). Only paths no
// other live conversation references are touched, and each one must sit
// directly in one of the agent's attachment directories: the paths come from
// persisted metadata, so they are re-validated here rather than trusted to
// address the filesystem. Thread cases are left alone because they belong to
// the IM thread, not to any one conversation.
//
// Best effort: a failure is logged and never fails the delete that triggered
// it — the conversation is already gone from the user's point of view.
func RemoveConversationUploads(ctx context.Context, st store.Store, ref store.ConversationRef) int {
	if st == nil || ref.ID == "" || ref.UserID == "" {
		return 0
	}
	paths, err := st.ExclusiveAttachmentPaths(ctx, ref.ID)
	if err != nil {
		log.Printf("[uploads] list attachments of deleted conversation %s: %v", ref.ID, err)
		return 0
	}
	if len(paths) == 0 {
		return 0
	}
	agentUser, err := st.GetUser(ctx, ref.UserID)
	if err != nil {
		log.Printf("[uploads] resolve agent %s of deleted conversation %s: %v", ref.UserID, ref.ID, err)
		return 0
	}
	homeOwner, err := AgentHomeOwner(ctx, st, agentUser)
	if err != nil {
		log.Printf("[uploads] deleted conversation %s: %v", ref.ID, err)
		return 0
	}
	scope, err := AgentScopeDir(agentUser.ID)
	if err != nil {
		return 0
	}
	scopeAbs := DaymugPath(homeOwner.WorkDir, scope)
	removed := 0
	for _, p := range paths {
		abs, ok := conversationUploadPath(homeOwner.WorkDir, scope, scopeAbs, p)
		if !ok {
			continue
		}
		if err := os.Remove(abs); err != nil {
			if !os.IsNotExist(err) {
				log.Printf("[uploads] remove %s of deleted conversation %s: %v", abs, ref.ID, err)
			}
			continue
		}
		removed++
	}
	return removed
}

// RemoveConversationsUploads is RemoveConversationUploads over a batch, in the
// shape the store's inactive-conversation hook delivers.
func RemoveConversationsUploads(ctx context.Context, st store.Store, refs []store.ConversationRef) {
	removed := 0
	for _, ref := range refs {
		removed += RemoveConversationUploads(ctx, st, ref)
	}
	if removed > 0 {
		log.Printf("[uploads] removed %d attachment file(s) of %d deleted conversation(s)", removed, len(refs))
	}
}

// runUploadCleanup runs the attachment cleanup a request-path delete
// triggers. Off the request because the overlap check scans every live
// message's metadata — seconds on a large database, per conversation — and the
// user already has their answer once the row is soft-deleted. Tests swap it for
// a synchronous call.
var runUploadCleanup = func(fn func()) { go fn() }

func removeUploadsInBackground(ctx context.Context, st store.Store, refs []store.ConversationRef) {
	if len(refs) == 0 {
		return
	}
	ctx = context.WithoutCancel(ctx)
	runUploadCleanup(func() { RemoveConversationsUploads(ctx, st, refs) })
}

// conversationUploadPath resolves a recorded attachment path to the file to
// delete, or reports false when it must be left alone: anything not exactly one
// directory below the agent scope, anything in the case directory, anything
// that resolves outside the scope through a symlink, and anything that is not
// a regular file.
func conversationUploadPath(homeRoot, scope, scopeAbs, recorded string) (string, bool) {
	clean := path.Clean(filepath.ToSlash(strings.TrimSpace(recorded)))
	dir, base := path.Split(clean)
	dir = strings.TrimSuffix(dir, "/")
	if base == "" || base == "." || base == ".." || path.Dir(dir) != scope || path.Base(dir) == CaseSegment {
		return "", false
	}
	abs := DaymugPath(homeRoot, clean)
	if within, err := PathWithin(scopeAbs, abs); err != nil || !within {
		return "", false
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return abs, true
}
