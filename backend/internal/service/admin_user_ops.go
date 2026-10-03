package service

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// AdminUserOps owns the admin-only write use-cases behind /api/admin/users:
// provisioning a login-capable human, patching one, resetting a password,
// enable/disable/delete, and the four batch controls the admin table exposes.
//
// Like UserOps and PromptRunner it is a bare struct with public fields and no
// constructor: the handler assembles one per call from its own live fields.
// AdminHandler receives its Cfg through NewAdminHandler today, but a snapshot
// taken at construction time would still be wrong for any field routes.go
// assigns afterwards, and the whole package follows the same lazy-factory rule.
//
// What deliberately stays at the transport boundary: the :id lookup and the
// "target must be a human" gate (handler.loadHuman), which resolve a path
// param and therefore belong to HTTP, and the read-only config/database
// endpoints, which are projections rather than use-cases.
type AdminUserOps struct {
	Store store.Store
	// Cfg is the live server config, consulted to validate provider bindings
	// and to resolve default_home_root. nil-safe throughout: a nil Cfg skips
	// provider validation and disables the work_dir default, which is the
	// pre-existing handler contract (unit tests construct handlers without a
	// config).
	Cfg *config.Config
}

// AdminCreateUserParams is the field set of a new login-capable user. It is the
// service-side mirror of the handler's JSON body: binding tags, and therefore
// the wire format, stay at the transport boundary.
type AdminCreateUserParams struct {
	Username string
	Password string
	Name     string
	Email    string
	IsAdmin  bool
	WorkDir  string
	// ProviderBindings maps CLI type to provider name; empty values clear the
	// slot.
	ProviderBindings map[string]string
	// ProviderAccounts grants a SET of accounts per CLI type (default-first).
	// A type present here is authoritative and supersedes the single-value
	// path for that type.
	ProviderAccounts map[string][]string
	// SandboxMode is the agent isolation mode. Anything other than
	// "unrestricted" normalises to jailed.
	SandboxMode string
}

// AdminUserPatch is the admin edit of an existing human. Every scalar is a
// pointer so the caller can distinguish "don't touch" (nil) from "set to the
// zero value"; the two maps use presence of a key for the same purpose.
type AdminUserPatch struct {
	Name             *string
	Email            *string
	WorkDir          *string
	ProviderBindings map[string]string
	ProviderAccounts map[string][]string
	IsAdmin          *bool
	DefaultModel     *string
	SandboxMode      *string
}

// EmailLocalPart returns everything before the first '@', or "" when the input
// carries no local-part. It is the single implementation of the
// username==email-prefix invariant's parsing half, shared by the admin create
// path, first-run bootstrap, and OIDC provisioning.
func EmailLocalPart(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return email[:i]
	}
	return ""
}

// usernamePattern is the shape a login username may take. The username names
// the user's home directory (<default_home_root>/<username>), so it must be a
// single plain path segment: no separators, no leading dot (which also rules
// out "." and ".."), nothing a shell or URL would need quoting for.
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidateUsername rejects usernames that cannot safely name a home directory.
// Every path that creates a login-capable user with a form-supplied username
// calls it; OIDC provisioning sanitises its derived name instead.
func ValidateUsername(username string) error {
	if !usernamePattern.MatchString(username) {
		return BadRequest("username (the part of the email before '@') must start with a letter or digit and contain only letters, digits, '.', '_' or '-' (at most 64 characters)")
	}
	return nil
}

// DefaultHomeDir returns the conventional home for a login-capable user:
// <users.default_home_root>/<username>. Returns "" when no config is loaded,
// which callers read as "no default available" — the admin create path turns
// that into a "work_dir is required" error rather than inventing a path.
//
// Note the deliberate non-check: a configured-but-empty default_home_root
// yields a *relative* path here (filepath.Join("", "alice") == "alice").
// Callers that require an absolute work_dir must say so themselves; this
// preserves the historical behaviour of both call sites.
func DefaultHomeDir(cfg *config.Config, username string) string {
	if cfg == nil {
		return ""
	}
	return filepath.Join(cfg.Users.DefaultHomeRoot, username)
}

// EnsureHomeDir creates a user's home directory so their first session starts
// from a valid root. Shared by admin create and OIDC auto-provisioning — the
// one step those two genuinely have in common.
//
// TODO(tx): this filesystem write is not atomic with the row insert that
// follows it. A mkdir that succeeds before a failing CreateUser leaves an
// orphan directory behind. Pre-existing behaviour, deliberately unchanged.
func EnsureHomeDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

// validateProviderBindings rejects entries whose (name, type) pair has no
// matching provider in the live config. Empty provider names are skipped: they
// encode "clear the slot" and need not resolve to anything.
func (o *AdminUserOps) validateProviderBindings(bindings map[string]string) error {
	if o.Cfg == nil {
		return nil
	}
	for ptype, pname := range bindings {
		if pname == "" {
			continue
		}
		if o.Cfg.FindAccountForType(pname, ptype) == nil {
			return BadRequest("unknown provider " + pname + " for type " + ptype)
		}
	}
	return nil
}

// validateProviderAccounts rejects any (name, type) in the account sets that has
// no matching provider in the live config. Blank names are skipped.
func (o *AdminUserOps) validateProviderAccounts(accounts map[string][]string) error {
	if o.Cfg == nil {
		return nil
	}
	for ptype, names := range accounts {
		for _, pname := range names {
			if strings.TrimSpace(pname) == "" {
				continue
			}
			if o.Cfg.FindAccountForType(strings.TrimSpace(pname), ptype) == nil {
				return BadRequest("unknown provider " + pname + " for type " + ptype)
			}
		}
	}
	return nil
}

// requireSomeAccount rejects a new user that would start with no account at
// all — such a user can log in but every chat is refused until an admin binds
// one. Skipped when no provider is configured (nothing could be bound) and on a
// nil Cfg, matching the other validators.
func (o *AdminUserOps) requireSomeAccount(bindings map[string]string, accounts map[string][]string) error {
	if _, ok := o.Cfg.InitialBindingProvider(); !ok {
		return nil
	}
	for _, name := range bindings {
		if strings.TrimSpace(name) != "" {
			return nil
		}
	}
	for _, names := range accounts {
		if len(cleanAccountNames(names)) > 0 {
			return nil
		}
	}
	return BadRequest("bind at least one account")
}

// validateDefaultModel accepts an empty string (clears the per-user default) or
// any model id that resolves to a configured provider.
//
// Deliberately NOT UserOps.NormalizeDefaultModel: that helper short-circuits on
// a nil Cfg, whereas the admin surface has always validated even without one —
// ProviderForModelForConfig falls back to the built-in per-type model registry,
// so a typo is caught in configless deployments too. Unifying the two would
// silently widen what the admin API accepts.
func (o *AdminUserOps) validateDefaultModel(model string) error {
	if model == "" {
		return nil
	}
	if ProviderForModelForConfig(o.Cfg, model) == "" {
		return BadRequest("unknown model " + model)
	}
	return nil
}

// cleanAccountNames trims each entry and drops blanks, preserving order. The
// first survivor is the type's default when the caller wants one.
func cleanAccountNames(raw []string) []string {
	names := make([]string, 0, len(raw))
	for _, n := range raw {
		if t := strings.TrimSpace(n); t != "" {
			names = append(names, t)
		}
	}
	return names
}

// requireHumans verifies every id names an existing login-capable user before
// any batch write runs, so a bad id in the middle of the list cannot leave a
// half-applied batch.
func (o *AdminUserOps) requireHumans(ctx context.Context, ids []string) error {
	for _, id := range ids {
		u, err := o.Store.GetUser(ctx, id)
		if err != nil || u.Username == "" {
			return NotFound("user not found: " + id)
		}
	}
	return nil
}

// CreateHuman provisions a new login-capable user and returns the row as
// constructed (not as re-read — the caller has always been answered with the
// local value, which is also the only place the hashed password never appears).
//
// work_dir defaults to default_home_root/<username> when not supplied, and is
// created on disk so the user has a valid root from their first session. The
// username is used rather than the opaque uuid so the layout stays readable.
//
// Enforces the username==email-prefix invariant: every login-capable row must
// have a non-empty email whose local-part equals the username. This is an
// admin-form ergonomics rule (one field, one predictable home directory), not an
// ownership mechanism — agents resolve to their owner through agents.owner_id.
// SSO auto-provisioning therefore does not share it: it cannot bounce a valid
// login over a name collision, so it seeds from the local-part and disambiguates.
//
// TODO(tx): several store writes plus one filesystem side effect with no
// transaction around them. A mkdir that succeeds before a failing CreateUser
// leaves an orphan directory; a SetUserProviderAccounts that fails mid-loop
// leaves the row created with only some account sets applied, and a default
// Agent failure leaves the human row behind. Pre-existing behaviour,
// deliberately unchanged here.
func (o *AdminUserOps) CreateHuman(ctx context.Context, p AdminCreateUserParams) (store.User, error) {
	username := strings.TrimSpace(p.Username)
	name := strings.TrimSpace(p.Name)
	email := strings.TrimSpace(p.Email)
	if email == "" {
		return store.User{}, BadRequest("email is required")
	}
	expectedUsername := EmailLocalPart(email)
	if expectedUsername == "" {
		return store.User{}, BadRequest("email must contain a local-part before '@'")
	}
	// Auto-derive username from email when omitted; otherwise require an exact
	// match. The invariant is non-negotiable, so a friendly default beats
	// bouncing a request that forgot the field but supplied a valid email.
	if username == "" {
		username = expectedUsername
	} else if username != expectedUsername {
		return store.User{}, BadRequest("username must equal the local-part of email (everything before '@')")
	}
	if err := ValidateUsername(username); err != nil {
		return store.User{}, err
	}
	if name == "" {
		name = username
	}
	if p.Password == "" {
		return store.User{}, BadRequest("password is required")
	}
	// Validate every binding against the live config before any write.
	bindings := maps.Clone(p.ProviderBindings)
	if err := o.validateProviderBindings(bindings); err != nil {
		return store.User{}, err
	}
	if err := o.validateProviderAccounts(p.ProviderAccounts); err != nil {
		return store.User{}, err
	}
	if err := o.requireSomeAccount(bindings, p.ProviderAccounts); err != nil {
		return store.User{}, err
	}
	// Reject duplicate usernames upfront for a clean error message; the partial
	// unique index would catch it too but the SQL error is opaque.
	if existing, err := o.Store.GetUserByUsername(ctx, username); err == nil && existing.ID != "" {
		return store.User{}, Conflict("username already exists")
	}
	// Same belt-and-suspenders for email — the store's partial unique index
	// enforces it too, but email is the OIDC login key so a clean 409 beats an
	// opaque constraint error.
	if existing, err := o.Store.GetUserByEmail(ctx, email); err == nil && existing.ID != "" {
		return store.User{}, Conflict("email already exists")
	}

	workDir := strings.TrimSpace(p.WorkDir)
	if workDir == "" {
		workDir = DefaultHomeDir(o.Cfg, username)
	}
	if workDir == "" {
		return store.User{}, BadRequest("work_dir is required (no default_home_root configured)")
	}
	if !filepath.IsAbs(workDir) {
		return store.User{}, BadRequest("work_dir must be absolute")
	}
	if err := EnsureHomeDir(workDir); err != nil {
		return store.User{}, Internal("create work_dir: "+err.Error(), err)
	}

	hash, err := HashPassword(p.Password)
	if err != nil {
		return store.User{}, Internal(err.Error(), err)
	}

	user := store.User{
		ID:           uuid.New().String(),
		Name:         name,
		Username:     username,
		PasswordHash: hash,
		Email:        email,
		IsAdmin:      p.IsAdmin,
		WorkDir:      workDir,
		// Hand the validated bindings to the store. CreateUser writes these
		// into user_provider_bindings inside the same DB session as the row
		// insert, so a fresh row never observes a moment of "user exists but
		// has no bindings yet" from the read side.
		ProviderBindings: bindings,
		SandboxMode:      p.SandboxMode,
	}
	// Normalise here as well as in the store: the response body is this local
	// value, so an unnormalised mode would be echoed back to the admin UI.
	if user.SandboxMode != store.SandboxModeUnrestricted {
		user.SandboxMode = store.SandboxModeJailed
	}
	if err := o.Store.CreateUser(ctx, user); err != nil {
		return store.User{}, Internal(err.Error(), err)
	}
	if _, err := CreateDefaultAgent(ctx, o.Store, user); err != nil {
		return store.User{}, err
	}
	// Multi-account sets override the single default CreateUser just wrote for
	// each listed type (SetUserProviderAccounts replaces the type wholesale).
	for ptype, rawNames := range p.ProviderAccounts {
		if err := o.Store.SetUserProviderAccounts(ctx, user.ID, ptype, cleanAccountNames(rawNames), ""); err != nil {
			return store.User{}, Internal(err.Error(), err)
		}
	}
	return user, nil
}

// UpdateHuman applies admin-only field changes to a human the transport layer
// already loaded and gated, and returns the row as persisted (with Env stripped
// — the admin table never renders per-user environment).
//
// authedUID is the acting admin, needed only for the self-demotion guard.
//
// TODO(tx): each patched field is its own store write with no transaction
// around the set; a failure halfway through leaves the earlier fields applied.
// Pre-existing behaviour, deliberately unchanged.
func (o *AdminUserOps) UpdateHuman(ctx context.Context, target store.User, p AdminUserPatch, authedUID string) (store.User, error) {
	if p.Name != nil && *p.Name != "" && *p.Name != target.Name {
		target.Name = *p.Name
		if err := o.Store.UpdateUser(ctx, target); err != nil {
			return store.User{}, Internal(err.Error(), err)
		}
	}
	if p.Email != nil {
		newEmail := strings.TrimSpace(*p.Email)
		if newEmail == "" {
			return store.User{}, BadRequest("email cannot be cleared on a human user")
		}
		expected := EmailLocalPart(newEmail)
		if expected == "" {
			return store.User{}, BadRequest("email must contain a local-part before '@'")
		}
		if expected != target.Username {
			return store.User{}, BadRequest("email local-part must equal the existing username (this binding is the invariant the upload feature relies on)")
		}
		if newEmail != target.Email {
			if existing, err := o.Store.GetUserByEmail(ctx, newEmail); err == nil && existing.ID != target.ID {
				return store.User{}, Conflict("email already in use by another user")
			}
		}
		if err := o.Store.SetUserEmail(ctx, target.ID, newEmail); err != nil {
			return store.User{}, Internal(err.Error(), err)
		}
	}
	if p.WorkDir != nil {
		wd := strings.TrimSpace(*p.WorkDir)
		if wd == "" || !filepath.IsAbs(wd) {
			return store.User{}, BadRequest("work_dir must be a non-empty absolute path")
		}
		if err := EnsureHomeDir(wd); err != nil {
			return store.User{}, Internal("create work_dir: "+err.Error(), err)
		}
		if err := o.Store.SetUserWorkDir(ctx, target.ID, wd); err != nil {
			return store.User{}, Internal(err.Error(), err)
		}
	}
	// Provider-binding patches, validated as one.
	// Switching a user's binding never migrates on-disk session logs: each
	// conversation is permanently pinned to the account it first ran on (see
	// ResolveConversationAccount), so its history stays put and only
	// new/unpinned conversations pick up the new default.
	patch := maps.Clone(p.ProviderBindings)
	// A type managed by the authoritative provider_accounts path must not also
	// be touched by the single-value path — drop overlaps so we don't apply
	// twice.
	for ptype := range p.ProviderAccounts {
		delete(patch, ptype)
	}
	if len(patch) > 0 {
		if err := o.validateProviderBindings(patch); err != nil {
			return store.User{}, err
		}
		// Trim whitespace on every value so " default" doesn't masquerade as a
		// distinct binding from "default".
		for k, v := range patch {
			patch[k] = strings.TrimSpace(v)
		}
		// Apply every patched slot — set/upsert when non-empty, delete when
		// empty. The store's SetUserProviderBinding handles both.
		for ptype, pname := range patch {
			if err := o.Store.SetUserProviderBinding(ctx, target.ID, ptype, pname); err != nil {
				return store.User{}, Internal(err.Error(), err)
			}
		}
	}
	// Authoritative multi-account patch. Each listed type's full account set is
	// replaced; the list's first non-blank entry is its default. No on-disk
	// migration — see the binding-patch note above.
	if len(p.ProviderAccounts) > 0 {
		if err := o.validateProviderAccounts(p.ProviderAccounts); err != nil {
			return store.User{}, err
		}
		for ptype, rawNames := range p.ProviderAccounts {
			names := cleanAccountNames(rawNames)
			newDefault := ""
			if len(names) > 0 {
				newDefault = names[0]
			}
			if err := o.Store.SetUserProviderAccounts(ctx, target.ID, ptype, names, newDefault); err != nil {
				return store.User{}, Internal(err.Error(), err)
			}
		}
	}
	if p.IsAdmin != nil {
		// Don't let an admin demote themselves — easy to lock yourself out. A
		// different admin can still do it.
		if !*p.IsAdmin && target.ID == authedUID {
			return store.User{}, BadRequest("cannot revoke your own admin flag")
		}
		if err := o.Store.SetUserAdmin(ctx, target.ID, *p.IsAdmin); err != nil {
			return store.User{}, Internal(err.Error(), err)
		}
	}
	if p.DefaultModel != nil {
		model := strings.TrimSpace(*p.DefaultModel)
		if err := o.validateDefaultModel(model); err != nil {
			return store.User{}, err
		}
		if err := o.Store.SetUserDefaultModel(ctx, target.ID, model); err != nil {
			return store.User{}, Internal(err.Error(), err)
		}
	}
	if p.SandboxMode != nil {
		if *p.SandboxMode != store.SandboxModeJailed && *p.SandboxMode != store.SandboxModeUnrestricted {
			return store.User{}, BadRequest("sandbox_mode must be 'jailed' or 'unrestricted'")
		}
		if err := o.Store.SetUserSandboxMode(ctx, target.ID, *p.SandboxMode); err != nil {
			return store.User{}, Internal(err.Error(), err)
		}
	}

	updated, err := o.Store.GetUser(ctx, target.ID)
	if err != nil {
		return store.User{}, Internal(err.Error(), err)
	}
	updated.Env = ""
	return updated, nil
}

// SetPassword resets a human's password to a fresh hash of plain.
func (o *AdminUserOps) SetPassword(ctx context.Context, target store.User, plain string) error {
	if plain == "" {
		return BadRequest("password is required")
	}
	hash, err := HashPassword(plain)
	if err != nil {
		return Internal(err.Error(), err)
	}
	if err := o.Store.SetUserPassword(ctx, target.ID, hash); err != nil {
		return Internal(err.Error(), err)
	}
	return nil
}

// SetDisabled enables or disables a human's ability to sign in. Disabling
// yourself is refused — it is an easy way to lock the last admin out.
func (o *AdminUserOps) SetDisabled(ctx context.Context, target store.User, disabled bool, authedUID string) error {
	if disabled && target.ID == authedUID {
		return BadRequest("cannot disable yourself")
	}
	if err := o.Store.SetUserDisabled(ctx, target.ID, disabled); err != nil {
		return Internal(err.Error(), err)
	}
	return nil
}

// DeleteHuman removes a login-capable user. Deleting yourself is refused for the
// same lock-out reason as SetDisabled.
func (o *AdminUserOps) DeleteHuman(ctx context.Context, target store.User, authedUID string) error {
	if target.ID == authedUID {
		return BadRequest("cannot delete yourself")
	}
	if err := o.Store.DeleteUser(ctx, target.ID); err != nil {
		return StoreError(err, "user not found")
	}
	return nil
}

// BatchSetDefaultModel sets (or clears, when model is empty) the per-user
// default model for every id in one call. Returns the trimmed model so the
// caller can echo it back. The model and every id are validated before any
// write, so a bad input cannot leave a half-applied batch.
//
// TODO(tx): the writes themselves are still a plain loop — a store failure on
// the third id leaves the first two applied. Pre-existing behaviour.
func (o *AdminUserOps) BatchSetDefaultModel(ctx context.Context, ids []string, model string) (string, error) {
	if len(ids) == 0 {
		return "", BadRequest("user_ids is required")
	}
	model = strings.TrimSpace(model)
	if err := o.validateDefaultModel(model); err != nil {
		return "", err
	}
	if err := o.requireHumans(ctx, ids); err != nil {
		return "", err
	}
	for _, id := range ids {
		if err := o.Store.SetUserDefaultModel(ctx, id, model); err != nil {
			return "", Internal(err.Error(), err)
		}
	}
	return model, nil
}

// BatchSetSandboxMode sets the agent isolation mode for every id. Unrestricted
// lets the user's agent reach the whole host filesystem; jailed confines it to
// the user's work_dir once the server-side bwrap sandbox is enabled.
//
// TODO(tx): partial application on a mid-loop store failure, as above.
func (o *AdminUserOps) BatchSetSandboxMode(ctx context.Context, ids []string, mode string) error {
	if len(ids) == 0 {
		return BadRequest("user_ids is required")
	}
	if mode != store.SandboxModeJailed && mode != store.SandboxModeUnrestricted {
		return BadRequest("sandbox_mode must be 'jailed' or 'unrestricted'")
	}
	if err := o.requireHumans(ctx, ids); err != nil {
		return err
	}
	for _, id := range ids {
		if err := o.Store.SetUserSandboxMode(ctx, id, mode); err != nil {
			return Internal(err.Error(), err)
		}
	}
	return nil
}

// BatchSetProviderBinding replaces the accounts for one provider type on every
// id. The first account is the default; an empty list clears the type. Returns
// the normalized type and names so the caller can echo them.
//
// Bindings only: unlike the single-user edit this never migrates Claude project
// files across config dirs.
//
// TODO(tx): partial application on a mid-loop store failure, as above.
func (o *AdminUserOps) BatchSetProviderBinding(ctx context.Context, ids []string, providerType string, providerNames []string) (string, []string, error) {
	if len(ids) == 0 {
		return "", nil, BadRequest("user_ids is required")
	}
	providerType = strings.TrimSpace(providerType)
	if providerType == "" {
		return "", nil, BadRequest("provider_type is required")
	}
	names := make([]string, 0, len(providerNames))
	seen := make(map[string]struct{}, len(providerNames))
	for _, rawName := range providerNames {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return "", nil, BadRequest("provider_names must not contain empty names")
		}
		if _, ok := seen[name]; ok {
			return "", nil, BadRequest("provider_names must not contain duplicates")
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	if err := o.validateProviderAccounts(map[string][]string{providerType: names}); err != nil {
		return "", nil, err
	}
	if err := o.requireHumans(ctx, ids); err != nil {
		return "", nil, err
	}
	defaultName := ""
	if len(names) > 0 {
		defaultName = names[0]
	}
	for _, id := range ids {
		if err := o.Store.SetUserProviderAccounts(ctx, id, providerType, names, defaultName); err != nil {
			return "", nil, Internal(err.Error(), err)
		}
	}
	return providerType, names, nil
}
