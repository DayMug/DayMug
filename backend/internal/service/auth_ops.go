package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/userenv"
)

// DefaultSessionTTL is how long a session stays valid when the deployment has
// not configured auth.session_ttl. It matches config's own default so an
// install that never touches the key behaves identically whether the session
// is opened through password login, first-run bootstrap, or OIDC.
const DefaultSessionTTL = 30 * 24 * time.Hour

// AuthOps holds the self-service auth use-cases: password login, session
// creation, password rotation, and the two per-user settings writes that the
// signed-in human performs on their own row.
//
// Deliberately a bare struct with public fields and no constructor — callers
// assemble it per request from whatever they already hold, which keeps the
// transport layer free to fill its own fields after construction (see the
// lazy-factory note on handler.AuthHandler.authOps).
//
// Nothing here ever sees a *gin.Context: the caller resolves the acting user
// id at the HTTP boundary and passes it in, and failures come back as
// *ServiceError so the transport layer owns the single status/body mapping.
type AuthOps struct {
	Store store.Store
	// Cfg is the live server config. Optional — a nil Cfg means "no policy
	// configured", which reads as password login enabled and the default
	// session TTL, matching how the handlers used to guard every access with
	// `if h.Cfg != nil`.
	Cfg *config.Config
}

// SessionTTL resolves the session lifetime: the configured auth.session_ttl
// when the operator set a positive value, otherwise DefaultSessionTTL. Exported
// because the transport layer needs the same number for the cookie's Max-Age.
func (o *AuthOps) SessionTTL() time.Duration {
	if o.Cfg != nil && o.Cfg.Auth.SessionTTL.Duration > 0 {
		return o.Cfg.Auth.SessionTTL.Duration
	}
	return DefaultSessionTTL
}

// passwordLoginEnabled reports whether the deployment still accepts local
// passwords. A nil Cfg is treated as enabled so test wiring that constructs a
// handler without config keeps working.
func (o *AuthOps) passwordLoginEnabled() bool {
	return o.Cfg == nil || o.Cfg.Auth.PasswordLoginEnabled
}

// invalidCredentials is the single response for every failed login, whatever
// the real cause. Wrong username, wrong password, and disabled account all
// produce identical output so an attacker cannot enumerate accounts by
// comparing responses.
func invalidCredentials() *ServiceError {
	return NewServiceError(http.StatusUnauthorized, "invalid credentials", nil)
}

// rawInternal reports a server-side failure whose client-facing text is the
// underlying error's own message. Several auth endpoints have always surfaced
// the raw store/crypto error verbatim; keeping Msg empty makes ServiceError
// render exactly that, so the response body is unchanged by the move into this
// package. Use Internal() instead whenever the cause should stay hidden.
func rawInternal(err error) *ServiceError {
	return NewServiceError(http.StatusInternalServerError, "", err)
}

// Login validates a username/password pair and, on success, opens a session for
// the user. It returns the user row (the caller renders the public subset), the
// opaque session token, and the session's expiry.
//
// The caller is responsible for turning the token into a cookie — cookie
// attributes are a transport concern and stay in the HTTP adapter.
func (o *AuthOps) Login(ctx context.Context, username, password string) (store.User, string, time.Time, error) {
	if !o.passwordLoginEnabled() {
		return store.User{}, "", time.Time{}, Forbidden("password login disabled")
	}
	if username == "" || password == "" {
		return store.User{}, "", time.Time{}, BadRequest("username and password required")
	}

	user, err := o.Store.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.User{}, "", time.Time{}, invalidCredentials()
		}
		return store.User{}, "", time.Time{}, rawInternal(err)
	}

	if !VerifyPassword(user.PasswordHash, password) {
		return store.User{}, "", time.Time{}, invalidCredentials()
	}
	// Same shape as a wrong password so a caller cannot tell disabled from
	// nonexistent from wrong-password. The operator who disabled the account
	// already knows what is going on.
	if user.Disabled {
		return store.User{}, "", time.Time{}, invalidCredentials()
	}

	token, expiresAt, err := o.OpenSession(ctx, user.ID)
	if err != nil {
		return store.User{}, "", time.Time{}, err
	}
	return user, token, expiresAt, nil
}

// OpenSession mints a session token for userID and persists it with the
// resolved TTL. This is the single implementation behind all three ways a
// session comes into existence — password login, first-run admin bootstrap,
// and OIDC callback — which previously each carried their own copy and had
// drifted: only the OIDC copy honoured auth.session_ttl, the other two pinned
// 30 days. Everything now reads the configured value (falling back to the same
// 30-day default), so a deployment that shortens session_ttl no longer has
// password logins quietly outliving SSO logins.
func (o *AuthOps) OpenSession(ctx context.Context, userID string) (string, time.Time, error) {
	token, err := NewSessionToken()
	if err != nil {
		return "", time.Time{}, rawInternal(err)
	}
	expiresAt := time.Now().Add(o.SessionTTL())
	if err := o.Store.CreateSession(ctx, store.Session{
		Token:     token,
		UserID:    userID,
		ExpiresAt: expiresAt,
	}); err != nil {
		return "", time.Time{}, rawInternal(err)
	}
	return token, expiresAt, nil
}

// ChangePassword rotates the signed-in user's own password. The current
// password is required as a re-auth check so a hijacked session cannot silently
// lock the real owner out. Refused when password login is disabled at the
// config level (OIDC-only deployments) or when the row has no password hash at
// all (an SSO-provisioned account has nothing to verify against, so this
// self-service flow does not apply to it).
func (o *AuthOps) ChangePassword(ctx context.Context, userID, currentPassword, newPassword string) error {
	if !o.passwordLoginEnabled() {
		return Forbidden("password login disabled")
	}
	if currentPassword == "" || newPassword == "" {
		return BadRequest("current_password and new_password are required")
	}
	if newPassword == currentPassword {
		return BadRequest("new password must differ from current password")
	}

	user, err := o.Store.GetUser(ctx, userID)
	if err != nil {
		return rawInternal(err)
	}
	if user.PasswordHash == "" || !VerifyPassword(user.PasswordHash, currentPassword) {
		return NewServiceError(http.StatusUnauthorized, "current password is incorrect", nil)
	}

	hash, err := HashPassword(newPassword)
	if err != nil {
		// 400, not 500: HashPassword only fails on caller-supplied input
		// (empty password, or a value past bcrypt's length limit).
		return NewServiceError(http.StatusBadRequest, err.Error(), err)
	}
	if err := o.Store.SetUserPassword(ctx, user.ID, hash); err != nil {
		return rawInternal(err)
	}
	return nil
}

// UpdateNotifications writes the signed-in user's notification settings.
// Notification config lives on the human owner — agent rows do not carry their
// own; maybeNotify resolves a conversation's user to its owner before reading
// these fields — so the caller always passes its own authenticated id.
//
// channel disambiguates which channel sends the push when both barkURL and
// pushDeerKey are set. Allowed values are "", "bark", "pushdeer"; anything else
// is rejected so a stale enum value cannot silently disable notifications.
//
// The three writes are not transactional at the store level, but validating
// every field before the first write means the only way to land half-applied
// state is an actual store failure mid-sequence, not a rejected input.
func (o *AuthOps) UpdateNotifications(ctx context.Context, userID, barkURL, pushDeerKey, channel string) error {
	switch channel {
	case "", "bark", "pushdeer":
	default:
		return BadRequest(`notification_channel must be one of: "", "bark", "pushdeer"`)
	}

	if err := o.Store.SetUserBarkURL(ctx, userID, barkURL); err != nil {
		// Only the first write distinguishes a missing row: it is the one
		// that proves the user id is real, so the later two can only fail
		// for infrastructure reasons.
		if errors.Is(err, store.ErrNotFound) {
			return &ServiceError{Status: http.StatusNotFound, Msg: "user not found", Err: err}
		}
		return rawInternal(err)
	}
	if err := o.Store.SetUserPushDeerKey(ctx, userID, pushDeerKey); err != nil {
		return rawInternal(err)
	}
	if err := o.Store.SetUserNotificationChannel(ctx, userID, channel); err != nil {
		return rawInternal(err)
	}
	return nil
}

// UpdateEnvironment validates and stores one VAR=VAL assignment per line for
// the given user, returning the normalized text that was persisted so the
// caller can echo back exactly what is now on the row. The id always comes from
// the authenticated session, so a user cannot edit another user's process
// environment.
func (o *AuthOps) UpdateEnvironment(ctx context.Context, userID, env string) (string, error) {
	env = strings.TrimSpace(env)
	if _, err := userenv.Parse(env); err != nil {
		return "", NewServiceError(http.StatusBadRequest, err.Error(), err)
	}
	if err := o.Store.SetUserEnv(ctx, userID, env); err != nil {
		return "", StoreError(err, "user not found")
	}
	return env, nil
}
