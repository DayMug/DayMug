package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db                *sql.DB
	dbPath            string
	maintenanceOnce   sync.Once
	maintenanceCancel context.CancelFunc
	maintenanceDone   chan struct{}
	// purgeAfter is how long soft-deleted rows survive before the background
	// maintenance sweep erases them. Zero (the default) keeps them forever.
	purgeAfter atomic.Int64
	// inactiveAfter is how long a conversation may sit untouched before the
	// maintenance sweep soft-deletes it. Zero (the default) never does.
	inactiveAfter atomic.Int64
	// onInactiveDeleted, when set, receives every conversation the
	// maintenance sweep soft-deletes, so the files they own can follow.
	onInactiveDeleted atomic.Pointer[func(context.Context, []ConversationRef)]
}

// NewSQLiteStore opens (and if needed creates) the SQLite database at dbPath.
// Callers must supply a concrete path resolved from config; the in-memory
// sentinel ":memory:" (and "file::memory:..." variants) is honored verbatim
// for tests.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("db path must not be empty")
	}

	if dbPath != ":memory:" && !strings.HasPrefix(dbPath, "file::memory:") {
		if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create db dir %s: %w", dir, err)
			}
		}
	}

	db, err := sql.Open("sqlite", dsnWithPragmas(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}

	// database/sql opens connections without limit by default. Each SQLite
	// connection costs three fds (db + wal + shm) plus its own page cache, and
	// they compete for the single WAL write lock — so an unbounded pool buys no
	// extra write throughput while adding fd pressure alongside the WebSocket
	// connections and agent subprocess pipes. Queries here are short, so callers
	// past the cap queue briefly rather than being rejected.
	//
	// Safe against deadlock only because no code path holds a transaction while
	// acquiring a second connection. Transactions go through withTx, whose
	// contract spells this out; methods that also need s.db (e.g. the existence
	// check in SetUserProviderBinding) finish those queries before withTx.
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxOpenConns)
	db.SetConnMaxIdleTime(5 * time.Minute)

	return &SQLiteStore{db: db, dbPath: dbPath}, nil
}

// maxOpenConns bounds the SQLite connection pool. See NewSQLiteStore.
const maxOpenConns = 16

// dsnWithPragmas appends connection-scoped PRAGMAs to the DSN. modernc.org/
// sqlite runs `_pragma=...` query parameters on every new pool connection;
// db.Exec("PRAGMA …") only touches the first connection it happens to grab,
// so settings like busy_timeout silently fail to propagate to the rest of
// the pool.
//
//   - busy_timeout(5000): wait up to 5s for a contended write lock instead
//     of failing instantly with SQLITE_BUSY. Without this, three browser
//     tabs sending prompts at once trigger overlapping writes (dispatcher
//     ClaimPendingPromptByID + EnqueuePrompt + persist context_usage); one
//     of them gets bounced with "database is locked", processPrompt returns
//     without claiming the row, and the dispatcher's sweep silently marks
//     the prompt done — the user's UI is left stuck on "queued" forever.
//   - foreign_keys(1): SQLite ships with FK enforcement off by default.
//   - synchronous(NORMAL): in WAL mode this is still crash-safe for the
//     database; only a power loss can drop the last few commits. The default
//     FULL fsyncs the WAL on every commit, which every streamed message pays.
//   - _txlock=immediate: transactions take the write lock at BEGIN. Under
//     the default DEFERRED a transaction that reads before it writes could
//     have its snapshot invalidated by a concurrent commit and fail with
//     SQLITE_BUSY_SNAPSHOT, which busy_timeout does not cover; IMMEDIATE
//     waits on busy_timeout instead. Every withTx caller writes, so taking
//     the lock up front serializes nothing that wasn't already serialized.
func dsnWithPragmas(dbPath string) string {
	const pragmas = "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	sep := "?"
	if strings.Contains(dbPath, "?") {
		sep = "&"
	}
	return dbPath + sep + pragmas
}

func (s *SQLiteStore) Backend() string { return "sqlite" }

func (s *SQLiteStore) DatabaseSize(_ context.Context) (int64, error) {
	return s.dbFileSize(), nil
}

// softDeleteRetention is how long soft-deleted users, agents, and conversations
// survive before an explicit OptimizeDatabase erases them for good. The
// background sweep uses SetPurgeRetention instead and is off by default, so
// nothing a user deleted disappears unless an operator opts in or asks for it.
// Seven days is the shortest window that
// still covers "deleted it Friday, needed it Monday" — the only realistic
// recovery request, since there is no undelete in the UI and restoring means
// an operator running SQL by hand.
const softDeleteRetention = 7 * 24 * time.Hour

// OptimizeDatabase reclaims disk: it reaps expired login sessions, hard-deletes
// users, agents, and conversations aged past softDeleteRetention, then runs
// VACUUM so SQLite returns the freed pages to the operating system. SQLite
// never reclaims space on its own after DELETE, so the file otherwise grows
// monotonically — and because DeleteConversation only stamps deleted_at,
// without the purge step an operator can delete thousands of conversations,
// press "optimize", and get back nothing.
//
// VACUUM rewrites the entire DB file and cannot run inside an explicit
// transaction; we issue it directly on the connection. WAL is checkpoint-
// truncated on either side so the sidecar files don't keep stale pages alive.
func (s *SQLiteStore) OptimizeDatabase(ctx context.Context) (DBOptimizeResult, error) {
	res := DBOptimizeResult{BeforeBytes: s.dbFileSize()}

	n, err := s.deleteExpiredSessions(ctx)
	if err != nil {
		return res, fmt.Errorf("delete expired sessions: %w", err)
	}
	res.ExpiredSessionsDeleted = n

	cutoff := time.Now().Add(-softDeleteRetention)
	users, agents, conversations, err := s.purgeSoftDeletedUsers(ctx, cutoff)
	if err != nil {
		return res, err
	}
	res.PurgedUsers = users
	res.PurgedAgents = agents
	res.PurgedConversations = conversations

	n, err = s.purgeSoftDeletedConversations(ctx, cutoff)
	if err != nil {
		return res, err
	}
	res.PurgedConversations += n

	if _, err := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return res, fmt.Errorf("wal_checkpoint pre-vacuum: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
		return res, fmt.Errorf("vacuum: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return res, fmt.Errorf("wal_checkpoint post-vacuum: %w", err)
	}

	res.AfterBytes = s.dbFileSize()
	return res, nil
}

// purgeSoftDeletedUsers removes aged deleted accounts and agents together
// with every database row that belongs exclusively to them. The schema keeps
// most ownership links deliberately free of foreign keys, so the complete
// cleanup is explicit and transactional rather than relying on cascades.
func (s *SQLiteStore) purgeSoftDeletedUsers(ctx context.Context, cutoff time.Time) (users, agents, conversations int64, err error) {
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		users, agents, conversations, err = purgeSoftDeletedUsersTx(ctx, tx, cutoff)
		return err
	})
	if err != nil {
		return 0, 0, 0, err
	}
	return users, agents, conversations, nil
}

func purgeSoftDeletedUsersTx(ctx context.Context, tx *sql.Tx, cutoff time.Time) (int64, int64, int64, error) {
	const expiredUsers = `SELECT id FROM users WHERE deleted_at IS NOT NULL AND deleted_at < ?`
	const expiredAgents = `SELECT id FROM agents
WHERE (deleted_at IS NOT NULL AND deleted_at < ?)
   OR owner_id IN (` + expiredUsers + `)`
	// Older agent-table migrations left a soft-deleted users row behind with
	// the same id as the live agents row. The rows are two representations of
	// one principal, so deleting data merely because the legacy representation
	// expired destroys the live Agent's conversations. The obsolete row itself
	// is still removed below; only principal-owned data is protected while a
	// live counterpart exists.
	const expiredUserPrincipals = `SELECT users.id FROM users
WHERE users.deleted_at IS NOT NULL AND users.deleted_at < ?
  AND NOT EXISTS (
      SELECT 1 FROM agents
      WHERE agents.id = users.id AND agents.deleted_at IS NULL
  )`
	const expiredAgentPrincipals = `SELECT agents.id FROM agents
WHERE ((agents.deleted_at IS NOT NULL AND agents.deleted_at < ?)
    OR agents.owner_id IN (` + expiredUsers + `))
  AND NOT EXISTS (
      SELECT 1 FROM users
      WHERE users.id = agents.id AND users.deleted_at IS NULL
  )`
	const expiredPrincipals = expiredUserPrincipals + ` UNION ` + expiredAgentPrincipals
	const expiredConversations = `SELECT id FROM conversations WHERE user_id IN (` + expiredPrincipals + `)`

	statements := []struct {
		name string
		sql  string
		args []any
	}{
		{"messages", `DELETE FROM messages WHERE conversation_id IN (` + expiredConversations + `)`, []any{cutoff, cutoff, cutoff}},
		{"bot threads", `DELETE FROM bot_threads WHERE agent_id IN (` + expiredAgents + `) OR conversation_id IN (` + expiredConversations + `)`, []any{cutoff, cutoff, cutoff, cutoff, cutoff}},
		{"cron jobs", `DELETE FROM cron_jobs WHERE owner_id IN (` + expiredUsers + `) OR agent_id IN (` + expiredAgents + `)`, []any{cutoff, cutoff, cutoff}},
		{"bots", `DELETE FROM bots WHERE agent_id IN (` + expiredAgents + `)`, []any{cutoff, cutoff}},
		{"token usage", `DELETE FROM token_usage WHERE user_id IN (` + expiredPrincipals + `)`, []any{cutoff, cutoff, cutoff}},
		{"provider bindings", `DELETE FROM user_provider_bindings WHERE user_id IN (` + expiredPrincipals + `)`, []any{cutoff, cutoff, cutoff}},
		{"sessions", `DELETE FROM sessions WHERE user_id IN (` + expiredUsers + `)`, []any{cutoff}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			return 0, 0, 0, fmt.Errorf("purge %s of soft-deleted users: %w", statement.name, err)
		}
	}

	conversationResult, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE user_id IN (`+expiredPrincipals+`)`, cutoff, cutoff, cutoff)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("purge conversations of soft-deleted users: %w", err)
	}
	conversationCount, err := conversationResult.RowsAffected()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count purged conversations: %w", err)
	}

	agentResult, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE id IN (`+expiredAgents+`)`, cutoff, cutoff)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("purge agents of soft-deleted users: %w", err)
	}
	agentCount, err := agentResult.RowsAffected()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count purged agents: %w", err)
	}
	userResult, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id IN (`+expiredUsers+`)`, cutoff)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("purge soft-deleted users: %w", err)
	}
	userCount, err := userResult.RowsAffected()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count purged users: %w", err)
	}
	return userCount, agentCount, conversationCount, nil
}

// purgeSoftDeletedConversations erases conversations soft-deleted before
// cutoff, together with their messages. A pinned conversation is retained
// while its user or agent still exists and is not deleted: pinning is an
// explicit request to preserve that history beyond the normal recovery
// window. Pinned conversations with a missing or deleted owner are still
// purged so owner cleanup cannot leave durable orphaned data.
//
// Messages are deleted explicitly rather than left to the ON DELETE CASCADE on
// messages.conversation_id: the FK is only enforced while the foreign_keys
// PRAGMA holds, and migrateMessagesRoleConstraint turns it off around table
// rebuilds. Orphaned message rows would be invisible (every read path joins
// conversations) and unreclaimable — exactly the bug this function exists to
// fix — so the cascade is a backstop here, not the mechanism.
//
// Other tables that carry a conversation id (bot_threads.conversation_id,
// cron_jobs.last_conversation_id) are deliberately left alone: neither is a
// foreign key, and both already tolerate a dangling id because a soft-deleted
// conversation is equally unreadable to them. The IM bridge re-creates a
// conversation when its stored id no longer resolves (ensureConversation).
//
// One transaction so a mid-purge failure cannot leave messages without their
// conversation.
func (s *SQLiteStore) purgeSoftDeletedConversations(ctx context.Context, cutoff time.Time) (int64, error) {
	const expired = `SELECT id FROM conversations
WHERE deleted_at IS NOT NULL AND deleted_at < ?
  AND (pinned = 0 OR NOT (
        EXISTS (SELECT 1 FROM users WHERE users.id = conversations.user_id AND users.deleted_at IS NULL)
     OR EXISTS (SELECT 1 FROM agents WHERE agents.id = conversations.user_id AND agents.deleted_at IS NULL)
  ))`
	var n int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM messages WHERE conversation_id IN (`+expired+`)`, cutoff,
		); err != nil {
			return fmt.Errorf("purge messages of soft-deleted conversations: %w", err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE id IN (`+expired+`)`, cutoff)
		if err != nil {
			return fmt.Errorf("purge soft-deleted conversations: %w", err)
		}
		n, err = res.RowsAffected()
		if err != nil {
			return fmt.Errorf("count purged conversations: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// dbFileSize returns the total bytes occupied on disk by the SQLite database
// file plus any WAL/SHM sidecars. Returns 0 for in-memory databases or when
// stat fails — callers display the difference, so an unknown size simply
// shows as zero reclaimed.
func (s *SQLiteStore) dbFileSize() int64 {
	if s.dbPath == "" || s.dbPath == ":memory:" || strings.HasPrefix(s.dbPath, "file::memory:") {
		return 0
	}
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if fi, err := os.Stat(s.dbPath + suffix); err == nil {
			total += fi.Size()
		}
	}
	return total
}

// execSingleRowUpdate runs an UPDATE statement that targets exactly one row
// and translates a zero affected-rows result into ErrNotFound. Shared across
// the SetUser* setters so the "row not found" semantics stay consistent.
func (s *SQLiteStore) execSingleRowUpdate(ctx context.Context, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
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

func (s *SQLiteStore) Close() error {
	s.stopMaintenance()
	return s.db.Close()
}

// Ping proves the database is open and readable. It reads the schema table
// rather than running `SELECT 1`, which SQLite answers without touching the
// file — a deleted, truncated or unreadable database would still pass that.
func (s *SQLiteStore) Ping(ctx context.Context) error {
	var n int
	return s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master").Scan(&n)
}
