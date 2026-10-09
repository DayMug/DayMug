// OIDC integration: Authorization Code Flow with state + nonce, ID-token
// signature/nonce verification, UserInfo fetch, and a configurable
// access-control decision over groups / roles / permissions claims. Modelled
// after the JWT-Standard Casdoor pattern.
package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/DayMug/DayMug/backend/internal/config"
)

// OIDCService is the runtime side of OIDCConfig: it owns the discovery
// result, the oauth2 client, and an in-memory map of pending auth attempts
// (state -> nonce). One instance per process.
type OIDCService struct {
	cfg      *config.OIDCConfig
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config

	mu      sync.Mutex
	pending map[string]pendingAuth // state -> nonce + creation time
}

type pendingAuth struct {
	Nonce     string
	CreatedAt time.Time
}

// NewOIDCService discovers the issuer's metadata and builds an oauth2 client.
// Returns nil, nil when cfg.Enabled is false so callers can no-op cleanly. A
// non-nil error means the operator asked for OIDC but discovery failed —
// startup should abort so misconfiguration is loud.
func NewOIDCService(ctx context.Context, cfg *config.OIDCConfig) (*OIDCService, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discover %q: %w", cfg.Issuer, err)
	}
	s := &OIDCService{
		cfg:      cfg,
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.RedirectURI,
			Scopes:       cfg.Scopes,
		},
		pending: make(map[string]pendingAuth),
	}
	go s.gc(ctx)
	return s, nil
}

// gc evicts pending entries older than 10 minutes. The IdP round-trip is
// nearly always under a minute; anything older is an abandoned attempt and
// keeping it around just leaks memory.
func (s *OIDCService) gc(ctx context.Context) {
	if s == nil {
		return
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.mu.Lock()
			for k, v := range s.pending {
				if now.Sub(v.CreatedAt) > 10*time.Minute {
					delete(s.pending, k)
				}
			}
			s.mu.Unlock()
		}
	}
}

// AuthCodeURL stages a state+nonce pair and returns the URL the browser
// should redirect to. Each call creates a fresh pair; the caller doesn't
// need to thread anything through.
func (s *OIDCService) AuthCodeURL() string {
	state := randURLSafe(24)
	nonce := randURLSafe(24)
	s.mu.Lock()
	s.pending[state] = pendingAuth{Nonce: nonce, CreatedAt: time.Now()}
	s.mu.Unlock()
	return s.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.AccessTypeOffline)
}

// OIDCIdentity is the subset of OIDC claims daymug cares about after a
// successful login. Sub is the IdP's stable user identifier; the rest are
// best-effort fields the caller maps onto the local users row.
type OIDCIdentity struct {
	Sub               string
	PreferredUsername string
	Name              string
	Email             string
	Groups            []string
	Roles             []string
	Permissions       []string
}

// HandleCallback completes the auth code exchange. It enforces state, nonce,
// id_token signature, and the configured access policy in one place. On
// success it returns the resolved identity; on failure the error message is
// safe to surface to the user (it never leaks credentials, only policy
// rationale).
func (s *OIDCService) HandleCallback(ctx context.Context, state, code string) (*OIDCIdentity, error) {
	if s == nil {
		return nil, errors.New("oidc not enabled")
	}
	if state == "" || code == "" {
		return nil, errors.New("missing state or code")
	}

	s.mu.Lock()
	pa, ok := s.pending[state]
	if ok {
		delete(s.pending, state)
	}
	s.mu.Unlock()
	if !ok {
		return nil, errors.New("unknown or expired state")
	}

	tok, err := s.oauth.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}

	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return nil, errors.New("token response missing id_token")
	}
	idt, err := s.verifier.Verify(ctx, rawID)
	if err != nil {
		return nil, fmt.Errorf("id_token verify: %w", err)
	}
	if idt.Nonce != pa.Nonce {
		return nil, errors.New("id_token nonce mismatch")
	}

	var idClaims map[string]any
	if err := idt.Claims(&idClaims); err != nil {
		return nil, fmt.Errorf("decode id_token claims: %w", err)
	}

	// UserInfo carries the authorization data on Casdoor's JWT-Standard
	// token format (groups/roles/permissions). Failure is fatal here
	// because the access decision needs it.
	var uiClaims map[string]any
	ui, err := s.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok))
	if err != nil {
		return nil, fmt.Errorf("userinfo: %w", err)
	}
	if err := ui.Claims(&uiClaims); err != nil {
		return nil, fmt.Errorf("decode userinfo: %w", err)
	}

	if reason, ok := s.evaluateAccess(idClaims, uiClaims); !ok {
		return nil, fmt.Errorf("access denied: %s", reason)
	}

	id := &OIDCIdentity{
		Sub:               firstNonEmpty(stringClaim(idClaims, "sub"), stringClaim(uiClaims, "sub")),
		PreferredUsername: firstNonEmpty(stringClaim(uiClaims, "preferred_username"), stringClaim(idClaims, "preferred_username")),
		Name:              firstNonEmpty(stringClaim(uiClaims, "name"), stringClaim(idClaims, "name")),
		Email:             firstNonEmpty(stringClaim(uiClaims, "email"), stringClaim(idClaims, "email")),
		Groups:            mergeClaim(s.cfg.GroupClaim, idClaims, uiClaims),
		Roles:             mergeClaim(s.cfg.RoleClaim, idClaims, uiClaims),
		Permissions:       mergeClaim(s.cfg.PermissionClaim, idClaims, uiClaims),
	}
	return id, nil
}

// evaluateAccess runs the OR-across-dimensions policy. Returns ok=true and
// no reason on allow; ok=false with a human-readable reason on deny. When
// every Required* list is empty, any authenticated user is allowed.
func (s *OIDCService) evaluateAccess(idClaims, uiClaims map[string]any) (string, bool) {
	type check struct {
		kind     string
		claim    string
		required []string
		values   []string
	}
	checks := []check{
		{"groups", s.cfg.GroupClaim, s.cfg.RequiredGroups, mergeClaim(s.cfg.GroupClaim, idClaims, uiClaims)},
		{"roles", s.cfg.RoleClaim, s.cfg.RequiredRoles, mergeClaim(s.cfg.RoleClaim, idClaims, uiClaims)},
		{"permissions", s.cfg.PermissionClaim, s.cfg.RequiredPermissions, mergeClaim(s.cfg.PermissionClaim, idClaims, uiClaims)},
	}

	configured := 0
	for _, c := range checks {
		if len(c.required) > 0 {
			configured++
		}
	}
	if configured == 0 {
		return "", true
	}

	var misses []string
	for _, c := range checks {
		if len(c.required) == 0 {
			continue
		}
		if intersects(c.values, c.required) {
			return "", true
		}
		misses = append(misses, fmt.Sprintf(
			"%s [%s] ∩ required [%s] = ∅",
			c.kind, strings.Join(c.values, ", "), strings.Join(c.required, ", "),
		))
	}
	return strings.Join(misses, "; "), false
}

// mergeClaim reads `claim` from both id-token and userinfo claim maps and
// returns a deduped union. Casdoor's JWT-Standard mode only populates
// groups/roles/permissions on the userinfo response; permissive IdPs may
// echo the same data on the id token. We accept either source.
func mergeClaim(claim string, idClaims, uiClaims map[string]any) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, src := range []map[string]any{idClaims, uiClaims} {
		for _, v := range claimToStrings(src, claim) {
			if _, dup := seen[v]; dup {
				continue
			}
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// claimToStrings normalises a claim value to a string slice. Casdoor sends
// groups/roles as []string; some IdPs send a single comma-joined string or
// arrays of objects with a "name" field. Tolerate them all.
func claimToStrings(claims map[string]any, claim string) []string {
	v, ok := claims[claim]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			switch y := item.(type) {
			case string:
				if y != "" {
					out = append(out, y)
				}
			case map[string]any:
				for _, k := range []string{"name", "id", "slug"} {
					if s, ok := y[k].(string); ok && s != "" {
						out = append(out, s)
						break
					}
				}
			}
		}
		return out
	}
	return nil
}

func stringClaim(claims map[string]any, name string) string {
	if claims == nil {
		return ""
	}
	if v, ok := claims[name].(string); ok {
		return v
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func intersects(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(b))
	for _, v := range b {
		set[v] = struct{}{}
	}
	for _, v := range a {
		if _, ok := set[v]; ok {
			return true
		}
	}
	return false
}

func randURLSafe(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is unrecoverable in any meaningful sense;
		// the entire login flow depends on unguessable values here.
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
