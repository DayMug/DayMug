package store

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// schemaSnapshot returns every table/index/trigger definition in the database,
// sorted so two runs are directly comparable.
func schemaSnapshot(t *testing.T, s *SQLiteStore) []string {
	t.Helper()
	rows, err := s.db.Query("SELECT type, name, IFNULL(sql, '') FROM sqlite_master")
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var kind, name, sql string
		if err := rows.Scan(&kind, &name, &sql); err != nil {
			t.Fatalf("scan sqlite_master: %v", err)
		}
		out = append(out, kind+"\t"+name+"\t"+strings.Join(strings.Fields(sql), " "))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	sort.Strings(out)
	return out
}

// TestInit_IsIdempotent is the regression guard for the ordered migration list:
// a second Init on an already-migrated file database must succeed, leave the
// schema byte-identical, and not disturb existing rows. Uses a real file (not
// ":memory:") so the WAL/reopen path is exercised the way a server restart is.
func TestInit_ReRunPreservesSchemaAndData(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "daymug.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Init(); err != nil {
		t.Fatalf("first init: %v", err)
	}
	first := schemaSnapshot(t, s)
	if len(first) == 0 {
		t.Fatal("first init created no schema objects")
	}

	// Real data must survive a re-run: the migration list contains backfill
	// UPDATEs guarded by "only touch unmigrated rows", and a regression there
	// would silently rewrite live rows on every server start.
	ctx := context.Background()
	user := User{ID: "u1", Name: "Alice", Username: "alice", Email: "alice@example.com"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := s.CreateConversation(ctx, "c1", "hello", "u1", "/tmp", "codex", "gpt-5"); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := s.SaveMessage(ctx, Message{ID: "m1", ConversationID: "c1", Role: "user", Content: "hi", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("save message: %v", err)
	}

	if err := s.Init(); err != nil {
		t.Fatalf("second init: %v", err)
	}

	second := schemaSnapshot(t, s)
	if len(second) != len(first) {
		t.Fatalf("schema object count changed: %d then %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("schema drifted on re-init:\n first: %s\nsecond: %s", first[i], second[i])
		}
	}

	conv, err := s.GetConversation(ctx, "c1")
	if err != nil {
		t.Fatalf("get conversation after re-init: %v", err)
	}
	// provider/session_id/pin_order all have backfill UPDATEs in the list.
	if conv.Provider != "codex" {
		t.Errorf("provider rewritten by re-init: got %q, want %q", conv.Provider, "codex")
	}
	if conv.Title != "hello" {
		t.Errorf("title changed by re-init: got %q", conv.Title)
	}
	msgs, err := s.ListMessages(ctx, "c1", 10, 0)
	if err != nil {
		t.Fatalf("list messages after re-init: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "hi" {
		t.Fatalf("messages lost on re-init: %+v", msgs)
	}
}

// TestInit_FreshDatabaseMatchesReInitialised pins the ordering contract from the
// other direction: a brand-new database and one that has already been through
// Init must converge on exactly the same schema, so an upgraded install never
// diverges from a fresh install.
func TestInit_FreshDatabaseMatchesReInitialised(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	once, err := NewSQLiteStore(filepath.Join(dir, "once.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer func() { _ = once.Close() }()
	if err := once.Init(); err != nil {
		t.Fatalf("init once: %v", err)
	}

	twice, err := NewSQLiteStore(filepath.Join(dir, "twice.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer func() { _ = twice.Close() }()
	for i := range 3 {
		if err := twice.Init(); err != nil {
			t.Fatalf("init pass %d: %v", i+1, err)
		}
	}

	a, b := schemaSnapshot(t, once), schemaSnapshot(t, twice)
	if len(a) != len(b) {
		t.Fatalf("schema object count differs: fresh=%d repeated=%d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("schema differs:\n  fresh: %s\nrepeated: %s", a[i], b[i])
		}
	}
}

// The sidebar query runs on every page load; without an index on user_id it is
// a full scan of every conversation in the install.
func TestInit_ConversationListIndexServesTheSidebarQuery(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	var plan string
	rows, err := s.db.Query(
		"EXPLAIN QUERY PLAN SELECT id FROM conversations WHERE user_id = ? AND deleted_at IS NULL ORDER BY pinned DESC, CASE WHEN pinned = 1 THEN pin_order ELSE 0 END ASC, updated_at DESC, created_at DESC, id DESC",
		"u1")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "idx_conversations_user_list") {
		t.Errorf("sidebar query does not use the index:\n%s", plan)
	}
}
