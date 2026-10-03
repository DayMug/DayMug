package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// OIDCUserOps provisions local rows for SSO identities that have no match yet.
//
// This is deliberately NOT a variant of AdminUserOps.CreateHuman, and must not
// be folded into it. The two agree on almost nothing: this path sanitises a
// username the IdP chose (which the admin form would simply reject), derives
// its admin flag from the bootstrap list rather than from the request, sets no
// password at all, accepts no work_dir, provider-binding or sandbox input, and
// disambiguates a colliding username instead of refusing — because refusing
// here locks a real person out of the product, whereas the admin form's
// operator can just pick another name. Merging them would mean one function
// with a mode flag toggling every one of those decisions.
//
// The genuinely common parts are shared as functions instead:
// DefaultHomeDir/EnsureHomeDir for the home directory, EmailLocalPart for
// parsing the address.
type OIDCUserOps struct {
	Store store.Store
	Cfg   *config.Config
}

// usernameSafe is the character set kept when deriving a local username from an
// OIDC claim. Anything else collapses to '-', so a stray '@' or the '/' in
// Casdoor's "org/user" convention never reaches the username column — or, since
// the username names the home directory, the filesystem.
var usernameSafe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// maxUsernameSuffix bounds the disambiguation search in uniqueUsername. Fifty
// distinct humans whose emails all collapse to one local-part is a misconfigured
// IdP, not a case worth looping over — and an unbounded loop would be a DoS
// lever on a path reachable before authentication.
const maxUsernameSuffix = 50

// DeriveUsername returns the email local-part, sanitized to the username-safe
// character set. preferred_username and sub are not consulted: the local-part
// is stable and IdP-independent, whereas a display name can change under us and
// would silently rename a provisioned user's home directory on next login.
//
// The result is a starting point, not a guarantee — sanitising is lossy
// (alice+test and alice-test both yield alice-test), so uniqueUsername has the
// final say on what gets stored. An IdP that omits email yields "", and the
// caller reports that it cannot provision.
func DeriveUsername(id *OIDCIdentity) string {
	s := strings.TrimSpace(EmailLocalPart(id.Email))
	if s == "" {
		return ""
	}
	return strings.Trim(usernameSafe.ReplaceAllString(s, "-"), "-")
}

// IsBootstrapAdmin reports whether username is on the configured bootstrap
// list. This is what auto-promotes the very first SSO login of an admin, so a
// fresh deployment with password_login_enabled=false is not locked out.
func IsBootstrapAdmin(cfg *config.Config, username string) bool {
	if cfg == nil {
		return false
	}
	for _, u := range cfg.Admin.BootstrapUsernames {
		if u == username {
			return true
		}
	}
	return false
}

// uniqueUsername returns candidate if it's free, otherwise the first free
// "candidate-N".
//
// Suffixing used to be forbidden because owner resolution keyed off the email
// local-part, so a username that didn't equal it orphaned the row. Ownership is
// now the referential agents.owner_id pointer (see store.User.OwnerID and
// Store.GetOwner), which decoupled the two: the username is a login handle and
// a home-directory name, not an ownership key.
//
// That inverts the trade-off. Refusing the login locked a real person out over
// a name collision they cannot control — two IdP accounts genuinely can
// collapse to one username, either by sharing a local-part (ada@a.com vs
// ada@b.com) or because sanitising created the collision (alice+test@x.com →
// alice-test, colliding with alice-test@x.com). A suffixed username costs
// nothing by comparison: identity travels on the email, which is what the
// callback looks the user up by and what the store's partial unique index keeps
// unique.
//
// Suffixing is also what keeps DefaultHomeDir distinct per human. Handing two
// people the same username would hand them the same work_dir — a real
// cross-user leak, and the one outcome worse than a lockout.
func (o *OIDCUserOps) uniqueUsername(ctx context.Context, candidate string) (string, error) {
	for i := 1; i <= maxUsernameSuffix; i++ {
		attempt := candidate
		if i > 1 {
			attempt = fmt.Sprintf("%s-%d", candidate, i)
		}
		_, err := o.Store.GetUserByUsername(ctx, attempt)
		if errors.Is(err, store.ErrNotFound) {
			return attempt, nil
		}
		if err != nil {
			return "", Internal(err.Error(), err)
		}
	}
	return "", Conflict(fmt.Sprintf(
		"no free username derived from %q after %d attempts — ask an admin to provision this account",
		candidate, maxUsernameSuffix))
}

// ProvisionUser creates a local row for an SSO identity with no existing match.
// The email is stored exactly as the IdP issued it, because that — not the
// derived username — is what every later login resolves the row by.
//
// TODO(tx): the home directory is created before the row insert with no
// transaction around the pair, so a failing CreateUser leaves an orphan
// directory. Same pre-existing shape as the other provisioning paths.
func (o *OIDCUserOps) ProvisionUser(ctx context.Context, id *OIDCIdentity) (store.User, error) {
	base := DeriveUsername(id)
	if base == "" {
		return store.User{}, BadRequest("could not derive a username from email local-part")
	}
	username, err := o.uniqueUsername(ctx, base)
	if err != nil {
		return store.User{}, err
	}

	workDir := DefaultHomeDir(o.Cfg, username)
	if err := EnsureHomeDir(workDir); err != nil {
		return store.User{}, Internal(err.Error(), err)
	}

	name := id.Name
	if name == "" {
		name = username
	}

	user := store.User{
		ID:       uuid.New().String(),
		Name:     name,
		Username: username,
		Email:    id.Email,
		WorkDir:  workDir,
		IsAdmin:  IsBootstrapAdmin(o.Cfg, username),
	}
	// Every new user starts with at least one account: the "default" provider,
	// or the first one configured. Without it the first chat is refused until
	// an admin notices and binds one by hand.
	if p, ok := o.Cfg.InitialBindingProvider(); ok {
		user.ProviderBindings = map[string]string{p.Type: p.Name}
	}
	if err := o.Store.CreateUser(ctx, user); err != nil {
		return store.User{}, Internal(err.Error(), err)
	}
	if _, err := CreateDefaultAgent(ctx, o.Store, user); err != nil {
		return store.User{}, err
	}
	log.Printf("oidc: provisioned user id=%s username=%s email=%s admin=%v",
		user.ID, username, id.Email, user.IsAdmin)
	return user, nil
}
