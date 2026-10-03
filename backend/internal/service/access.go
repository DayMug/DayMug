package service

import (
	"context"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// CanAccessOwner reports whether the caller may act on resources owned by
// ownerID. Allowed when ownerID equals the authenticated user, or when the
// owner is an agent (an agents row) whose owner_id points back at
// the caller — agents are stamped with their creator's id at Create time so
// owner_id is the referential ownership pointer back to the human creator.
//
// Strict ownership: there is NO admin shortcut. Admins are subject to the
// same agent-visibility rules as everyone else; their elevated privileges
// are exercised through dedicated /api/admin/* endpoints, not by silently
// piggy-backing on the regular per-user routes. Orphan agents (Username
// == "" && OwnerID == "") are unreachable through the API for everyone —
// recovery is a direct DB op (UPDATE agents SET owner_id = …).
//
// When no auth context is set (unit tests bypass middleware), the check is
// skipped. In production every code path that calls this is guarded by
// RequireAuth, which guarantees a non-empty authedUID.
func CanAccessOwner(ctx context.Context, s store.Store, authedUID, ownerID string) bool {
	if authedUID == "" {
		return true
	}
	if ownerID == "" || ownerID == authedUID {
		return true
	}
	if s == nil {
		return false
	}
	authed, err := s.GetUser(ctx, authedUID)
	if err != nil {
		return false
	}
	owner, err := s.GetUser(ctx, ownerID)
	if err != nil {
		return false
	}
	caller := authed.Owner()
	return caller != "" && caller == owner.Owner()
}

// CanAccessChatOwner is the stricter variant for chat/conversation ownership.
// Humans may authenticate and own agents, but conversations must be attached to
// an agent row (Username == "", OwnerID != ""). This keeps stale human user ids
// from old URLs/localStorage from minting conversations that disappear after a
// refresh, because the picker only exposes agents.
func CanAccessChatOwner(ctx context.Context, s store.Store, authedUID, ownerID string) bool {
	if authedUID == "" {
		return true
	}
	if !CanAccessOwner(ctx, s, authedUID, ownerID) || s == nil {
		return false
	}
	owner, err := s.GetUser(ctx, ownerID)
	if err != nil {
		return false
	}
	return owner.Username == "" && owner.OwnerID != ""
}
