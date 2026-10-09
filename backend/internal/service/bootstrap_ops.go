package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// BootstrapOps owns first-run admin provisioning — the one account that has to
// be creatable before any account exists, and therefore before any session can
// authorise the call.
//
// Separate from AdminUserOps rather than folded into CreateHuman because the
// two differ in almost everything that matters: this path grants admin
// unconditionally (it is the only way to get a first admin at all), accepts no
// work_dir override, no provider bindings, no sandbox mode, and answers a
// public endpoint. What they genuinely share — local-part parsing, the
// conventional home path, password hashing — is shared as functions.
//
// Bare struct with public fields and no constructor, assembled per call by the
// handler, same as the other *Ops types.
type BootstrapOps struct {
	Store store.Store
	// Cfg supplies default_home_root. A nil Cfg leaves no home to provision
	// under, which CreateFirstAdmin reports rather than guessing a path.
	Cfg *config.Config
}

// BootstrapAdminParams is the first-run form as submitted.
type BootstrapAdminParams struct {
	Username string
	Email    string
	Password string
	Name     string
}

// HasHumanUsers reports whether any login-capable row exists. Agents do not
// count: they cannot log in, so an agent-only database is still pre-bootstrap.
//
// This is both the login page's "divert to /setup" signal and the predicate
// that closes the public create endpoint once the install is past first run.
func (o *BootstrapOps) HasHumanUsers(ctx context.Context) (bool, error) {
	users, err := o.Store.ListUsers(ctx)
	if err != nil {
		return false, err
	}
	for _, u := range users {
		if u.Username != "" {
			return true, nil
		}
	}
	return false, nil
}

// OIDCProvisionsFirstAdmin reports whether SSO alone can bring up the first
// admin: a first login auto-provisions the row, and a bootstrap username turns
// it into an admin. When it holds, the local setup form is redundant — and
// leaving it public would let whoever reaches the site first claim admin
// before the owner's SSO login does.
func OIDCProvisionsFirstAdmin(cfg *config.Config) bool {
	return cfg != nil && cfg.OIDC.Enabled && cfg.OIDC.AutoProvision && len(cfg.Admin.BootstrapUsernames) > 0
}

// FirstAdminSetupMode says how a fresh install gets its first admin, given the
// sign-in methods the config enables.
type FirstAdminSetupMode string

const (
	// SetupModePassword: the setup form creates a password account.
	SetupModePassword FirstAdminSetupMode = "password"
	// SetupModeSSOEmail: password login is off but SSO is on without being able
	// to make an admin by itself. The form records only the admin's email; SSO
	// login matches existing users by email, so that is how they sign in later.
	// A password here would be dead weight the login endpoint always refuses.
	SetupModeSSOEmail FirstAdminSetupMode = "sso_email"
	// SetupModeSSO: SSO provisions the first admin (OIDCProvisionsFirstAdmin);
	// the public form stays closed.
	SetupModeSSO FirstAdminSetupMode = "sso"
	// SetupModeUnavailable: neither password login nor SSO is enabled, so no
	// account created now could ever sign in. The form refuses and says which
	// config to change, rather than minting an account that locks itself out.
	SetupModeUnavailable FirstAdminSetupMode = "unavailable"
)

// FirstAdminSetup picks the setup mode for cfg. A nil cfg behaves like the
// defaults (password login on).
func FirstAdminSetup(cfg *config.Config) FirstAdminSetupMode {
	switch {
	case OIDCProvisionsFirstAdmin(cfg):
		return SetupModeSSO
	case cfg == nil || cfg.Auth.PasswordLoginEnabled:
		return SetupModePassword
	case cfg.OIDC.Enabled:
		return SetupModeSSOEmail
	default:
		return SetupModeUnavailable
	}
}

// FormClosedError is the refusal for a mode with no setup form, or nil when
// the form is open.
func (m FirstAdminSetupMode) FormClosedError() error {
	switch m {
	case SetupModeSSO:
		return Forbidden("first-run setup is disabled: sign in with SSO to become admin")
	case SetupModeUnavailable:
		return Forbidden("no sign-in method is enabled: set auth.password_login_enabled: true or configure oidc in config.yaml, then restart")
	}
	return nil
}

// MinPasswordLength is the shortest password the setup form accepts.
// maxPasswordBytes is bcrypt's input limit; longer input would fail hashing
// with an opaque 500 instead of a clear 400.
const (
	MinPasswordLength = 8
	maxPasswordBytes  = 72
)

// ValidateNewPassword enforces the length bounds on a password being set.
func ValidateNewPassword(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return BadRequest(fmt.Sprintf("password must be at least %d characters", MinPasswordLength))
	}
	if len(password) > maxPasswordBytes {
		return BadRequest(fmt.Sprintf("password must be at most %d bytes", maxPasswordBytes))
	}
	return nil
}

// CreateFirstAdmin provisions the initial administrator and returns the row as
// constructed — which, as in CreateHuman, is also the only form in which the
// password hash never reaches a response.
//
// It enforces the same username==email-prefix rule as the admin form, because
// this account is created through a form too and the derived home directory
// should be predictable. The first-run gate itself lives in the store: the
// emptiness check, the admin row and its default Agent commit in one
// transaction, so concurrent submissions yield exactly one admin. The home
// directory is created first and removed again if the insert loses, so a
// failure leaves neither an orphan directory nor a human without its Agent.
func (o *BootstrapOps) CreateFirstAdmin(ctx context.Context, p BootstrapAdminParams) (store.User, error) {
	mode := FirstAdminSetup(o.Cfg)
	if err := mode.FormClosedError(); err != nil {
		return store.User{}, err
	}

	username := strings.TrimSpace(p.Username)
	email := strings.TrimSpace(p.Email)
	name := strings.TrimSpace(p.Name)

	if email == "" {
		return store.User{}, BadRequest("email is required")
	}
	expectedUsername := EmailLocalPart(email)
	if expectedUsername == "" {
		return store.User{}, BadRequest("email must contain a local-part before '@'")
	}
	// Derive when omitted, require an exact match otherwise — a friendly default
	// beats bouncing a form that supplied a valid email but skipped the field.
	if username == "" {
		username = expectedUsername
	} else if username != expectedUsername {
		return store.User{}, BadRequest("username must equal the local-part of email (everything before '@')")
	}
	if err := ValidateUsername(username); err != nil {
		return store.User{}, err
	}
	var hash string
	if mode == SetupModePassword {
		if p.Password == "" {
			return store.User{}, BadRequest("password is required")
		}
		if err := ValidateNewPassword(p.Password); err != nil {
			return store.User{}, err
		}
		var err error
		if hash, err = HashPassword(p.Password); err != nil {
			return store.User{}, Internal(err.Error(), err)
		}
	}
	if name == "" {
		name = username
	}

	workDir := DefaultHomeDir(o.Cfg, username)
	if workDir == "" {
		return store.User{}, Internal("no default_home_root configured; cannot provision admin", nil)
	}
	_, statErr := os.Stat(workDir)
	createdDir := errors.Is(statErr, fs.ErrNotExist)
	if err := EnsureHomeDir(workDir); err != nil {
		return store.User{}, Internal("create work_dir: "+err.Error(), err)
	}

	user := store.User{
		ID:           uuid.New().String(),
		Name:         name,
		Username:     username,
		PasswordHash: hash,
		Email:        email,
		IsAdmin:      true,
		WorkDir:      workDir,
	}
	// Same row CreateDefaultAgent builds. It is assembled here rather than
	// through UserOps.CreateAgent because that path reads the owner back from
	// the store, and the owner does not exist until the transaction commits.
	agent := store.User{
		ID:      uuid.New().String(),
		Name:    username,
		OwnerID: user.ID,
		WorkDir: workDir,
	}
	if err := o.Store.CreateFirstAdmin(ctx, user, agent); err != nil {
		// Undo the mkdir unless the winning submission claimed the same
		// username, in which case the directory is now its home. Remove, not
		// RemoveAll: only an empty directory goes.
		if createdDir {
			if _, lookupErr := o.Store.GetUserByUsername(ctx, username); lookupErr != nil {
				_ = os.Remove(workDir)
			}
		}
		if errors.Is(err, store.ErrSetupClosed) {
			return store.User{}, Conflict("an admin user already exists")
		}
		return store.User{}, Internal(err.Error(), err)
	}
	return user, nil
}
