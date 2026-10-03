package service

import (
	"errors"
	"fmt"
	"slices"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// Why a run could not be placed on an account. Match with errors.Is against a
// *RunAccountError; its Error() is the one user-facing wording every path
// (web chat, /compact, auto-title, session bundle) shares.
var (
	// ErrRunAccountRevoked: the conversation is pinned to an account the user
	// no longer has.
	ErrRunAccountRevoked = errors.New("pinned provider account revoked")
	// ErrRunAccountUnbound: the user has no binding for the provider type.
	ErrRunAccountUnbound = errors.New("no provider account bound")
	// ErrRunAccountUnresolvable: the bound name matches no configured account
	// of that type (deleted or re-typed), or there is no pool to resolve in.
	ErrRunAccountUnresolvable = errors.New("provider binding cannot be resolved")
)

// RunAccountError reports a failed account resolution.
type RunAccountError struct {
	// Kind is one of the ErrRunAccount* sentinels.
	Kind     error
	Provider string
	// Account is the pinned or bound name involved, when there is one.
	Account string
	// Err is the underlying lookup failure (unresolvable only).
	Err error
}

func (e *RunAccountError) Error() string {
	switch {
	case errors.Is(e.Kind, ErrRunAccountRevoked):
		return fmt.Sprintf("this conversation is bound to the %q account %q, which is no longer available on your account — ask an administrator to restore it or start a new conversation", e.Provider, e.Account)
	case errors.Is(e.Kind, ErrRunAccountUnbound):
		return fmt.Sprintf("no account bound for %q: ask an administrator to bind a provider to your account", e.Provider)
	default:
		if e.Err != nil {
			return "provider binding cannot be resolved: " + e.Err.Error()
		}
		return "provider binding cannot be resolved"
	}
}

func (e *RunAccountError) Is(target error) bool { return target == e.Kind }
func (e *RunAccountError) Unwrap() error        { return e.Err }

// ResolveConversationAccount picks the provider account a conversation runs
// against, enforcing that a conversation is permanently bound to the account
// it first ran on.
//
//   - Pinned to an account still in the user's allowed set → that account.
//   - Pinned to an account no longer in the set (the admin revoked it) → an
//     ErrRunAccountRevoked error. We deliberately do NOT fall back to the
//     user's default here: the conversation's history/rate-limit lives on the
//     pinned account, so silently re-homing it onto a different account would
//     corrupt --resume and cross-bill. The turn must fail loudly until an
//     admin restores the account (or the user starts a fresh conversation).
//   - Unpinned → the user's default account for the type. Empty string (no
//     error) when the user has no binding for the type at all; callers that
//     need a concrete account use ResolveRunAccount, which reports that as
//     ErrRunAccountUnbound.
func ResolveConversationAccount(user store.User, providerType, pinnedAccount string) (string, error) {
	if pinnedAccount != "" {
		if slices.Contains(user.ProviderAccounts[providerType], pinnedAccount) {
			return pinnedAccount, nil
		}
		return "", &RunAccountError{Kind: ErrRunAccountRevoked, Provider: providerType, Account: pinnedAccount}
	}
	if user.ProviderBindings != nil {
		return user.ProviderBindings[providerType], nil
	}
	return "", nil
}

// ResolveRunAccount resolves the account a run for user on providerType uses:
// the conversation's pin (pinnedAccount, "" for a stateless run) or else the
// user's binding, looked up in pool. The binding is mandatory — there is no
// implicit fallback to a default provider, which used to funnel every unbound
// user onto one shared login and trip its rate limit. Every failure is a
// *RunAccountError.
func ResolveRunAccount(pool *Pool, user store.User, providerType, pinnedAccount string) (*config.Provider, error) {
	boundName, err := ResolveConversationAccount(user, providerType, pinnedAccount)
	if err != nil {
		return nil, err
	}
	if boundName == "" {
		return nil, &RunAccountError{Kind: ErrRunAccountUnbound, Provider: providerType}
	}
	if pool == nil {
		return nil, &RunAccountError{Kind: ErrRunAccountUnresolvable, Provider: providerType, Account: boundName,
			Err: errors.New("no account pool wired")}
	}
	acc, err := pool.AccountForType(boundName, providerType)
	if err != nil {
		return nil, &RunAccountError{Kind: ErrRunAccountUnresolvable, Provider: providerType, Account: boundName, Err: err}
	}
	return acc, nil
}
