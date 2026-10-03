package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/middleware"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// loadOwnedConversation fetches a conversation by id and verifies the caller
// is allowed to access it. On any failure (not found, store error, forbidden)
// the appropriate JSON response is written and ok=false is returned so the
// caller can simply `return` without further error handling.
func loadOwnedConversation(c *gin.Context, s store.Store, id string) (store.Conversation, bool) {
	conv, err := s.GetConversation(c.Request.Context(), id)
	if err != nil {
		respondStoreError(c, err, "conversation not found")
		return store.Conversation{}, false
	}
	if !service.CanAccessOwner(c.Request.Context(), s, middleware.CurrentUserID(c), conv.UserID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return store.Conversation{}, false
	}
	return conv, true
}

// loadOwnedUser fetches a user by id and verifies the caller may act on it.
// The caller may always act on themselves; otherwise the target must be an
// agent (User row with no username) — agents are owned by whichever human is
// logged in. On any failure the appropriate JSON response is written and
// ok=false is returned.
func loadOwnedUser(c *gin.Context, s store.Store, id string) (store.User, bool) {
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing user id"})
		return store.User{}, false
	}
	user, err := s.GetUser(c.Request.Context(), id)
	if err != nil {
		respondStoreError(c, err, "user not found")
		return store.User{}, false
	}
	if !service.CanAccessOwner(c.Request.Context(), s, middleware.CurrentUserID(c), user.ID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return store.User{}, false
	}
	return user, true
}

// isPathWithin reports whether child resolves to parent or one of its
// descendants. Both inputs are made absolute and have their symlinks
// dereferenced before comparison, so trailing slashes, "." segments,
// relative paths and links planted inside the tree don't trip the check.
// Used to confine an agent's work_dir to its owning human's home — a
// string-prefix test would falsely accept "/home/alice2" as inside
// "/home/alice", so containment goes through filepath.Rel.
//
// Fails closed: an unresolvable path (dangling link, symlink loop, denied
// parent) counts as outside.
func isPathWithin(parent, child string) bool {
	if parent == "" || child == "" {
		return false
	}
	within, err := service.PathWithin(parent, child)
	return err == nil && within
}

// The requireWorkDirWithinCaller gin helper that used to live here — "an agent's
// work_dir must sit inside its owning human's home" — is now
// service.UserOps.EnsureWorkDirWithinCaller, so the rule applies identically
// whether a caller comes through HTTP or another service. Its responses are
// unchanged; the handler renders them via respondServiceError.

// respondStoreError translates a store error into the standard JSON response:
// 404 with notFoundMsg for ErrNotFound, otherwise a logged generic 500.
func respondStoreError(c *gin.Context, err error, notFoundMsg string) {
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": notFoundMsg})
		return
	}
	respondInternalError(c, "store", err)
}
