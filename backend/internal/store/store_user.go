package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const agentUserColumns = `id, name, '' AS username, '' AS password_hash, '' AS email, 0 AS is_admin, 0 AS disabled, owner_id,
	work_dir, avatar, role_definition,
	env, mcp_config, claude_md_content, manage_claude_md,
	'' AS bark_url, '' AS pushdeer_key, '' AS notification_channel, default_model, think_level, case_mode,
	sandbox_mode,
	created_at, sort_order, archived_at`

// userColumns projects a human row onto the shared User scan order. The
// Agent-only fields are literal placeholders so scanUser serves both tables.
const userColumns = `id, name, username, password_hash, email, is_admin, disabled, '' AS owner_id,
	work_dir, avatar, '' AS role_definition,
	env, '' AS mcp_config, '' AS claude_md_content, 0 AS manage_claude_md,
	bark_url, pushdeer_key, notification_channel, default_model, '' AS think_level, 0 AS case_mode,
	sandbox_mode,
	created_at, sort_order, NULL AS archived_at`

// scanUser scans the canonical userColumns projection into a User in the same
// order they appear in userColumns. ProviderBindings + Provider are
// derived from the user_provider_bindings table; callers populate them via
// attachUserBindings after this scan.
func scanUser(row interface{ Scan(...any) error }, u *User) error {
	var archivedAt sql.NullTime
	if err := row.Scan(&u.ID, &u.Name, &u.Username, &u.PasswordHash, &u.Email, &u.IsAdmin, &u.Disabled, &u.OwnerID,
		&u.WorkDir, &u.Avatar, &u.RoleDefinition,
		&u.Env, &u.McpConfig, &u.ClaudeMdContent, &u.ManageClaudeMd,
		&u.BarkURL, &u.PushDeerKey, &u.NotificationChannel, &u.DefaultModel, &u.ThinkLevel, &u.CaseMode,
		&u.SandboxMode,
		&u.CreatedAt, &u.SortOrder, &archivedAt); err != nil {
		return err
	}
	u.Archived = archivedAt.Valid
	return nil
}

func isAgentRow(user User) bool {
	return user.OwnerID != ""
}

// attachUserBindings loads the user's provider bindings and populates the
// User struct's ProviderBindings map and the derived Provider field.
//
// Agents (rows with empty Username) have no independent provider config —
// they borrow their human owner's bindings transparently, resolved through
// the owner_id pointer (the same invariant GetOwner uses). Any rows in
// user_provider_bindings keyed to an agent's id are ignored. An orphan
// agent (empty owner_id, e.g. its human was deleted) ends up with no
// bindings, which the terminal handler surfaces as "no account bound".
func (s *SQLiteStore) attachUserBindings(ctx context.Context, u *User) error {
	bindingsUserID := u.Owner()
	if bindingsUserID == "" {
		return nil
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT provider_type, provider_name, is_default FROM user_provider_bindings WHERE user_id = ? ORDER BY is_default DESC, provider_name ASC", bindingsUserID)
	if err != nil {
		return fmt.Errorf("query bindings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	bindings := map[string]string{}
	accounts := map[string][]string{}
	for rows.Next() {
		var ptype, pname string
		var isDefault int
		if err := rows.Scan(&ptype, &pname, &isDefault); err != nil {
			return fmt.Errorf("scan binding: %w", err)
		}
		accounts[ptype] = append(accounts[ptype], pname)
		if isDefault == 1 || bindings[ptype] == "" {
			bindings[ptype] = pname
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate bindings: %w", err)
	}
	if len(bindings) > 0 {
		u.ProviderBindings = bindings
		u.ProviderAccounts = accounts
	}
	return nil
}

// attachUserBindingsBatch hydrates ProviderBindings + Provider on a
// slice of users in one DB round-trip. Used by ListUsers so the admin
// listing doesn't issue N+1 queries against user_provider_bindings.
func (s *SQLiteStore) attachUserBindingsBatch(ctx context.Context, users []User) error {
	if len(users) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id, provider_type, provider_name, is_default FROM user_provider_bindings ORDER BY is_default DESC, provider_name ASC`)
	if err != nil {
		return fmt.Errorf("query bindings batch: %w", err)
	}
	defer func() { _ = rows.Close() }()
	byUser := map[string]map[string]string{}
	accountsByUser := map[string]map[string][]string{}
	for rows.Next() {
		var uid, ptype, pname string
		var isDefault int
		if err := rows.Scan(&uid, &ptype, &pname, &isDefault); err != nil {
			return fmt.Errorf("scan binding row: %w", err)
		}
		m, ok := byUser[uid]
		if !ok {
			m = map[string]string{}
			byUser[uid] = m
		}
		if isDefault == 1 || m[ptype] == "" {
			m[ptype] = pname
		}
		am, ok := accountsByUser[uid]
		if !ok {
			am = map[string][]string{}
			accountsByUser[uid] = am
		}
		am[ptype] = append(am[ptype], pname)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate bindings batch: %w", err)
	}
	// Agents borrow their human owner's bindings — same invariant as
	// attachUserBindings / GetOwner, resolved through owner_id. Orphan
	// agents (empty owner_id) have no owner row to borrow from and are
	// left unbound.
	for i := range users {
		srcID := users[i].Owner()
		if srcID == "" {
			continue
		}
		if m, ok := byUser[srcID]; ok {
			users[i].ProviderBindings = m
			users[i].ProviderAccounts = accountsByUser[srcID]
		}
	}
	return nil
}

func (s *SQLiteStore) CreateUser(ctx context.Context, user User) error {
	if user.ID == "" {
		user.ID = uuid.New().String()
	}
	if isAgentRow(user) {
		return insertAgentRow(ctx, s.db, user)
	}
	if err := insertHumanRow(ctx, s.db, user); err != nil {
		return err
	}
	// Initial provider bindings from admin create / auto-provision, written
	// as rows on user_provider_bindings so the new user is bound from the
	// first read.
	for ptype, pname := range user.ProviderBindings {
		if pname == "" {
			continue
		}
		if err := s.SetUserProviderBinding(ctx, user.ID, ptype, pname); err != nil {
			return fmt.Errorf("set initial binding %q: %w", ptype, err)
		}
	}
	return nil
}

// userExecer is the slice of *sql.DB / *sql.Tx the row inserts need, so the
// same statements serve a plain CreateUser and a transactional caller.
type userExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertAgentRow(ctx context.Context, ex userExecer, user User) error {
	if user.SandboxMode == "" {
		user.SandboxMode = SandboxModeJailed
	}
	_, err := ex.ExecContext(ctx,
		`INSERT INTO agents (id, owner_id, name, work_dir, avatar, role_definition,
		 env, mcp_config, claude_md_content, manage_claude_md,
		 sandbox_mode, default_model, think_level, case_mode, sort_order)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		user.ID, user.OwnerID, user.Name, user.WorkDir, user.Avatar, user.RoleDefinition,
		user.Env, user.McpConfig, user.ClaudeMdContent, user.ManageClaudeMd,
		user.SandboxMode, user.DefaultModel, user.ThinkLevel, user.CaseMode, user.SortOrder)
	return err
}

func insertHumanRow(ctx context.Context, ex userExecer, user User) error {
	if user.SandboxMode == "" {
		user.SandboxMode = SandboxModeJailed
	}
	_, err := ex.ExecContext(ctx,
		`INSERT INTO users (id, name, username, password_hash, email, is_admin, disabled,
		 work_dir, avatar, env, bark_url, pushdeer_key, notification_channel,
		 default_model, sandbox_mode)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		user.ID, user.Name, user.Username, user.PasswordHash, user.Email, user.IsAdmin, user.Disabled,
		user.WorkDir, user.Avatar, user.Env, user.BarkURL, user.PushDeerKey, user.NotificationChannel,
		user.DefaultModel, user.SandboxMode)
	return err
}

// CreateFirstAdmin inserts the first-run admin and its default Agent in one
// transaction, and only while no login-capable row exists. The emptiness check
// and the inserts share the transaction, so concurrent setup submissions
// cannot each pass the check and all become admins: SQLite serialises the
// writers, and a loser retried on a fresh snapshot sees the winner's row.
func (s *SQLiteStore) CreateFirstAdmin(ctx context.Context, admin, agent User) error {
	if admin.Username == "" || agent.OwnerID != admin.ID {
		return errors.New("CreateFirstAdmin: admin needs a username and agent must be owned by it")
	}
	return s.withTxRetry(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM users WHERE deleted_at IS NULL AND username != ''`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrSetupClosed
		}
		if err := insertHumanRow(ctx, tx, admin); err != nil {
			return err
		}
		return insertAgentRow(ctx, tx, agent)
	})
}

func (s *SQLiteStore) GetUser(ctx context.Context, id string) (User, error) {
	var u User
	err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = ? AND deleted_at IS NULL`, id), &u)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return User{}, err
		}
		if err := scanUser(s.db.QueryRowContext(ctx,
			`SELECT `+agentUserColumns+` FROM agents WHERE id = ? AND deleted_at IS NULL`, id), &u); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return User{}, ErrNotFound
			}
			return User{}, err
		}
	}
	if err := s.attachUserBindings(ctx, &u); err != nil {
		return User{}, err
	}
	return u, nil
}

func (s *SQLiteStore) GetUserByUsername(ctx context.Context, username string) (User, error) {
	if username == "" {
		return User{}, ErrNotFound
	}
	var u User
	err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = ? AND deleted_at IS NULL`, username), &u)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	if err := s.attachUserBindings(ctx, &u); err != nil {
		return User{}, err
	}
	return u, nil
}

// GetUserByEmail looks up a login-capable user by email. Used by the OIDC
// callback to bind an external identity to a local row. Empty email returns
// ErrNotFound (avoiding accidental hits on the many unsynced agent rows that
// share the empty default). Restricted to rows with a non-empty username so
// agents (which inherit the human's email but are not independently
// login-able) can never match.
func (s *SQLiteStore) GetUserByEmail(ctx context.Context, email string) (User, error) {
	if email == "" {
		return User{}, ErrNotFound
	}
	var u User
	err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE email = ? AND username != '' AND deleted_at IS NULL LIMIT 1`, email), &u)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	if err := s.attachUserBindings(ctx, &u); err != nil {
		return User{}, err
	}
	return u, nil
}

// GetOwner resolves the human owner of any user row. Human users are their
// own owner; agents (rows with empty username) resolve through their owner_id
// pointer to the owning human row. An agent with an empty owner_id, or whose
// owner_id points at a row that no longer exists (deleted human), returns
// ErrNotFound.
func (s *SQLiteStore) GetOwner(ctx context.Context, user User) (User, error) {
	if user.Username != "" {
		return user, nil
	}
	if user.OwnerID == "" {
		return User{}, ErrNotFound
	}
	owner, err := s.GetUser(ctx, user.OwnerID)
	if err != nil {
		return User{}, err
	}
	return owner, nil
}

func (s *SQLiteStore) ListUsers(ctx context.Context) ([]User, error) {
	// agents goes first: a compound SELECT takes its column types from the
	// first arm, and only agents has a real archived_at for the driver to
	// decode as a time — userColumns projects a typeless NULL there.
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+agentUserColumns+` FROM agents WHERE deleted_at IS NULL
		 UNION ALL
		 SELECT `+userColumns+` FROM users WHERE deleted_at IS NULL
		 ORDER BY sort_order ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var users []User
	for rows.Next() {
		var u User
		if err := scanUser(rows, &u); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.attachUserBindingsBatch(ctx, users); err != nil {
		return nil, err
	}
	return users, nil
}

// ReorderAgents makes `ids` the authoritative sidebar order for ownerID's own
// rows: each id is stamped with sort_order = its index. The caller's rows are
// their own human row (id == ownerID) plus the agents they own (owner_id ==
// ownerID, empty username), so the human owner can be reordered freely among its
// agents. Rows the caller doesn't own match no row and are silently skipped.
// Runs in one transaction so a concurrent ListUsers never sees a half-applied
// order.
func (s *SQLiteStore) ReorderAgents(ctx context.Context, ownerID string, ids []string) error {
	if ownerID == "" {
		return nil
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for i, id := range ids {
			if id == ownerID {
				if _, err := tx.ExecContext(ctx,
					"UPDATE users SET sort_order = ? WHERE id = ? AND deleted_at IS NULL",
					i, id); err != nil {
					return err
				}
				continue
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE agents SET sort_order = ? WHERE id = ? AND owner_id = ? AND deleted_at IS NULL",
				i, id, ownerID); err != nil {
				return err
			}
		}
		return nil
	})
}

// ArchiveUser hides an agent from the sidebar by stamping archived_at. The row
// stays on disk; the owner can restore it from settings. Idempotent on
// already-archived (or missing) rows: returns ErrNotFound rather than
// re-stamping the timestamp.
func (s *SQLiteStore) ArchiveUser(ctx context.Context, id string) error {
	now := time.Now()
	if err := s.execSingleRowUpdate(ctx,
		"UPDATE agents SET archived_at = ? WHERE id = ? AND archived_at IS NULL AND deleted_at IS NULL",
		now, id); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	return ErrNotFound
}

// UnarchiveUser clears archived_at, restoring the agent to the sidebar.
// Returns ErrNotFound when the row is missing or was not archived.
func (s *SQLiteStore) UnarchiveUser(ctx context.Context, id string) error {
	if err := s.execSingleRowUpdate(ctx,
		"UPDATE agents SET archived_at = NULL WHERE id = ? AND archived_at IS NOT NULL AND deleted_at IS NULL",
		id); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	return ErrNotFound
}

// ListArchivedAgents returns the archived agents owned by ownerID, ordered like
// the sidebar. Provider bindings are hydrated so the settings page can render
// the same agent rows the sidebar does.
func (s *SQLiteStore) ListArchivedAgents(ctx context.Context, ownerID string) ([]User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+agentUserColumns+` FROM agents WHERE owner_id = ? AND archived_at IS NOT NULL AND deleted_at IS NULL
		 ORDER BY sort_order ASC, created_at ASC`,
		ownerID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var users []User
	for rows.Next() {
		var u User
		if err := scanUser(rows, &u); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.attachUserBindingsBatch(ctx, users); err != nil {
		return nil, err
	}
	return users, nil
}

// SetUserPassword updates only the password_hash for a user. Used by the CLI
// `user passwd` subcommand and by login flows that need to upgrade hashes.
func (s *SQLiteStore) SetUserPassword(ctx context.Context, userID, passwordHash string) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE users SET password_hash = ? WHERE id = ?", passwordHash, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUser soft-deletes a user and every conversation they own. The rows
// stay on disk with `deleted_at` set so the API hides them but operators can
// still recover the data; the partial unique index on `username`/`email`
// excludes deleted rows so the slot is freed for re-creation.
//
// Already-deleted conversations are left untouched so their original
// deletion timestamp survives — re-stamping them with the user's deletion
// time would lose that history.
func (s *SQLiteStore) DeleteUser(ctx context.Context, id string) error {
	now := time.Now()
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM bots WHERE agent_id = ?", id); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			"UPDATE conversations SET deleted_at = ? WHERE user_id = ? AND deleted_at IS NULL",
			now, id); err != nil {
			return err
		}

		res, err := tx.ExecContext(ctx,
			"UPDATE users SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL",
			now, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			res, err = tx.ExecContext(ctx,
				"UPDATE agents SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL",
				now, id)
			if err != nil {
				return err
			}
			n, err = res.RowsAffected()
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrNotFound
			}
		}
		return nil
	})
}

// UpdateUser writes the per-row mutable fields a regular user (or agent
// owner) can change through the existing /users/:id endpoints. Admin-only
// fields (is_admin, disabled, provider bindings, email, work_dir, username,
// password_hash) and notification settings (bark_url — set via
// SetUserBarkURL on the human owner) are intentionally left out.
//
// Human rows intentionally carry only account fields; prompt and tool settings
// are persisted only by the agents branch.
func (s *SQLiteStore) UpdateUser(ctx context.Context, user User) error {
	if isAgentRow(user) {
		res, err := s.db.ExecContext(ctx,
			`UPDATE agents SET name = ?, avatar = ?, role_definition = ?,
			 mcp_config = ?, claude_md_content = ?, manage_claude_md = ?,
			 default_model = ?, think_level = ?, case_mode = ?
			 WHERE id = ? AND deleted_at IS NULL`,
			user.Name, user.Avatar, user.RoleDefinition,
			user.McpConfig, user.ClaudeMdContent, user.ManageClaudeMd,
			user.DefaultModel, user.ThinkLevel, user.CaseMode, user.ID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET name = ?, avatar = ?, default_model = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		user.Name, user.Avatar, user.DefaultModel, user.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserBarkURL writes the Bark notification URL for a user. Notification
// config is per-human-owner — see handler.maybeNotify, which resolves
// agent conversations to their human owner before reading the URL — so the
// caller is expected to invoke this against a human row.
func (s *SQLiteStore) SetUserBarkURL(ctx context.Context, userID, barkURL string) error {
	return s.execSingleRowUpdate(ctx, "UPDATE users SET bark_url = ? WHERE id = ?", barkURL, userID)
}

// SetUserPushDeerKey writes the PushDeer push key for a user. Lives on the
// human owner row alongside bark_url; agent conversations resolve to their
// owner before reading either field.
func (s *SQLiteStore) SetUserPushDeerKey(ctx context.Context, userID, pushDeerKey string) error {
	return s.execSingleRowUpdate(ctx, "UPDATE users SET pushdeer_key = ? WHERE id = ?", pushDeerKey, userID)
}

// SetUserNotificationChannel writes the user's explicit channel choice
// ("bark" / "pushdeer" / "") used to disambiguate when both channels are
// configured. Validation belongs to the caller — the store accepts any
// string so future channels can be added without a schema change.
func (s *SQLiteStore) SetUserNotificationChannel(ctx context.Context, userID, channel string) error {
	return s.execSingleRowUpdate(ctx, "UPDATE users SET notification_channel = ? WHERE id = ?", channel, userID)
}

// SetUserAdmin flips the is_admin flag. Used by bootstrap on startup and by
// the admin user-management endpoints.
func (s *SQLiteStore) SetUserAdmin(ctx context.Context, userID string, isAdmin bool) error {
	return s.execSingleRowUpdate(ctx, "UPDATE users SET is_admin = ? WHERE id = ?", isAdmin, userID)
}

// SetUserDisabled blocks/unblocks login without deleting the user's data.
func (s *SQLiteStore) SetUserDisabled(ctx context.Context, userID string, disabled bool) error {
	return s.execSingleRowUpdate(ctx, "UPDATE users SET disabled = ? WHERE id = ?", disabled, userID)
}

// SetUserWorkDir is admin-only; regular users have no UI to change their own
// workdir (per the security requirement that they never see absolute paths).
func (s *SQLiteStore) SetUserWorkDir(ctx context.Context, userID, workDir string) error {
	// Must skip soft-deleted rows: an agent moved out of users by an old
	// migration can still have a soft-deleted users row under the same id
	// beside its live agents row. Without `deleted_at IS NULL` this
	// UPDATE would hit the dead users row, report one row affected, and
	// return success — silently leaving the real agents.work_dir unchanged,
	// so the edit appears to save but never takes effect. Mirrors the
	// deleted_at filter GetUser/UpdateUser already use.
	if err := s.execSingleRowUpdate(ctx, "UPDATE users SET work_dir = ? WHERE id = ? AND deleted_at IS NULL", workDir, userID); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	return s.execSingleRowUpdate(ctx, "UPDATE agents SET work_dir = ? WHERE id = ? AND deleted_at IS NULL", workDir, userID)
}

// GetUserProviderBindings returns the {cliType → default providerName} map —
// one entry per type (the type's default account). Empty map (not nil) when
// the user has no bindings.
func (s *SQLiteStore) GetUserProviderBindings(ctx context.Context, userID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT provider_type, provider_name, is_default FROM user_provider_bindings WHERE user_id = ? ORDER BY is_default DESC, provider_name ASC", userID)
	if err != nil {
		return nil, fmt.Errorf("query bindings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var ptype, pname string
		var isDefault int
		if err := rows.Scan(&ptype, &pname, &isDefault); err != nil {
			return nil, fmt.Errorf("scan binding: %w", err)
		}
		if isDefault == 1 || out[ptype] == "" {
			out[ptype] = pname
		}
	}
	return out, rows.Err()
}

// GetUserProviderAccounts returns the {cliType → []providerName} set the user
// may use, default account first within each type. Empty map (not nil) when
// the user has no bindings.
func (s *SQLiteStore) GetUserProviderAccounts(ctx context.Context, userID string) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT provider_type, provider_name FROM user_provider_bindings WHERE user_id = ? ORDER BY is_default DESC, provider_name ASC", userID)
	if err != nil {
		return nil, fmt.Errorf("query accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var ptype, pname string
		if err := rows.Scan(&ptype, &pname); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		out[ptype] = append(out[ptype], pname)
	}
	return out, rows.Err()
}

// SetUserProviderBinding makes providerName the default account for its type,
// adding the row if absent and demoting any other accounts of that type to
// non-default (their rows are preserved). Empty providerName routes to
// DeleteUserProviderBinding, clearing every account for the type. Returns
// ErrNotFound when the referenced user does not exist.
func (s *SQLiteStore) SetUserProviderBinding(ctx context.Context, userID, providerType, providerName string) error {
	if providerName == "" {
		return s.DeleteUserProviderBinding(ctx, userID, providerType)
	}
	var present int
	if err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM users WHERE id = ? AND deleted_at IS NULL
		 UNION ALL
		 SELECT 1 FROM agents WHERE id = ? AND deleted_at IS NULL
		 LIMIT 1`, userID, userID).Scan(&present); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("check user existence: %w", err)
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"UPDATE user_provider_bindings SET is_default = 0 WHERE user_id = ? AND provider_type = ?",
			userID, providerType); err != nil {
			return fmt.Errorf("demote existing defaults: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO user_provider_bindings (user_id, provider_type, provider_name, is_default, updated_at)
VALUES (?, ?, ?, 1, datetime('now'))
ON CONFLICT(user_id, provider_type, provider_name) DO UPDATE SET
  is_default = 1,
  updated_at = excluded.updated_at
`, userID, providerType, providerName); err != nil {
			return fmt.Errorf("upsert binding: %w", err)
		}
		return nil
	})
}

// SetUserProviderAccounts replaces the user's allowed account set for one CLI
// type. names is the full set (duplicates and blanks ignored); defaultName —
// when non-empty and present in names — becomes the type's default, otherwise
// the first name wins. Empty names clears the type. Returns ErrNotFound when
// the user does not exist.
func (s *SQLiteStore) SetUserProviderAccounts(ctx context.Context, userID, providerType string, names []string, defaultName string) error {
	var present int
	if err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM users WHERE id = ? AND deleted_at IS NULL
		 UNION ALL
		 SELECT 1 FROM agents WHERE id = ? AND deleted_at IS NULL
		 LIMIT 1`, userID, userID).Scan(&present); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("check user existence: %w", err)
	}
	// De-dup while preserving order; drop blanks.
	seen := map[string]bool{}
	clean := make([]string, 0, len(names))
	for _, n := range names {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		clean = append(clean, n)
	}
	if defaultName == "" || !seen[defaultName] {
		if len(clean) > 0 {
			defaultName = clean[0]
		}
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM user_provider_bindings WHERE user_id = ? AND provider_type = ?",
			userID, providerType); err != nil {
			return fmt.Errorf("clear accounts: %w", err)
		}
		for _, n := range clean {
			isDefault := 0
			if n == defaultName {
				isDefault = 1
			}
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO user_provider_bindings (user_id, provider_type, provider_name, is_default, updated_at) VALUES (?, ?, ?, ?, datetime('now'))",
				userID, providerType, n, isDefault); err != nil {
				return fmt.Errorf("insert account %q: %w", n, err)
			}
		}
		return nil
	})
}

// DeleteUserProviderBinding removes the (userID, providerType) row. No-op
// when no row matches so callers can idempotently "clear" a slot.
func (s *SQLiteStore) DeleteUserProviderBinding(ctx context.Context, userID, providerType string) error {
	if _, err := s.db.ExecContext(ctx,
		"DELETE FROM user_provider_bindings WHERE user_id = ? AND provider_type = ?",
		userID, providerType); err != nil {
		return fmt.Errorf("delete binding: %w", err)
	}
	return nil
}

// SetUserEmail updates the email address used for OIDC matching.
func (s *SQLiteStore) SetUserEmail(ctx context.Context, userID, email string) error {
	return s.execSingleRowUpdate(ctx, "UPDATE users SET email = ? WHERE id = ?", email, userID)
}

// SetUserEnv writes the self-managed environment text for agent processes.
func (s *SQLiteStore) SetUserEnv(ctx context.Context, userID, env string) error {
	if err := s.execSingleRowUpdate(ctx, "UPDATE users SET env = ? WHERE id = ?", env, userID); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	return s.execSingleRowUpdate(ctx, "UPDATE agents SET env = ? WHERE id = ? AND deleted_at IS NULL", env, userID)
}

// SetUserDefaultModel writes the per-user default model new conversations
// inherit. Empty clears the default; the store does no validation (the admin
// handler checks the id resolves to a configured provider before calling).
func (s *SQLiteStore) SetUserDefaultModel(ctx context.Context, userID, model string) error {
	return s.execSingleRowUpdate(ctx, "UPDATE users SET default_model = ? WHERE id = ?", model, userID)
}

// SetUserSandboxMode writes the per-user agent isolation mode. Any value other
// than SandboxModeUnrestricted is normalised to SandboxModeJailed so the
// column never holds a value that SandboxUnrestricted would have to guess at.
func (s *SQLiteStore) SetUserSandboxMode(ctx context.Context, userID, mode string) error {
	if mode != SandboxModeUnrestricted {
		mode = SandboxModeJailed
	}
	if err := s.execSingleRowUpdate(ctx, "UPDATE users SET sandbox_mode = ? WHERE id = ?", mode, userID); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	return s.execSingleRowUpdate(ctx, "UPDATE agents SET sandbox_mode = ? WHERE id = ? AND deleted_at IS NULL", mode, userID)
}
