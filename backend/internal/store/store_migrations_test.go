package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var updateMigrations = flag.Bool("update-migrations", false, "append newly added steps to testdata/migration_steps.golden")

const migrationStepsGoldenPath = "testdata/migration_steps.golden"

// v1_5_109Fresh is a fresh database exactly as baselineRelease left it: every
// step recorded except the deferred users cleanup (94), whose columns and
// trigger are still present.
//
//go:embed testdata/v1.5.109-fresh.sql
var v1_5_109Fresh string

// ledgerCompleteFinish is what ledgerCompleteRelease did to such a database on
// its first start: the deferred users cleanup, recorded as version 94. Applied
// on top of v1_5_109Fresh it yields the oldest database this binary upgrades.
var ledgerCompleteFinish = []string{
	"DROP TRIGGER disable_cron_jobs_for_archived_legacy_agent",
	"ALTER TABLE users DROP COLUMN skills",
	"ALTER TABLE users DROP COLUMN role_definition",
	"ALTER TABLE users DROP COLUMN permission_rules",
	"ALTER TABLE users DROP COLUMN mcp_config",
	"ALTER TABLE users DROP COLUMN claude_md_content",
	"ALTER TABLE users DROP COLUMN manage_claude_md",
	"ALTER TABLE users DROP COLUMN owner_id",
	"ALTER TABLE users DROP COLUMN source",
	"ALTER TABLE users DROP COLUMN allow_cli_mode",
	"ALTER TABLE users DROP COLUMN allow_tty_mode",
	"INSERT INTO schema_migrations (version, name, destructive) VALUES (94, 'trim legacy user agent columns', 1)",
}

// migrationStepLines renders the post-baseline steps the way the ledger sees
// them: schema_migrations stores only a version number per applied step, so
// version N must name the same step forever.
func migrationStepLines() []string {
	lines := make([]string, len(migrationSteps))
	for i, step := range migrationSteps {
		kind := "additive"
		if step.destructive {
			kind = "destructive"
		}
		lines[i] = fmt.Sprintf("%03d\t%s\t%s", baselineVersion+i+1, kind, strings.Join(strings.Fields(step.desc), " "))
	}
	return lines
}

// TestMigrationStepsSnapshot keeps shipped migration steps from being renamed,
// edited, reordered or removed. Any of those silently desynchronises databases
// that already recorded the old step under its version number: Init skips it
// by version and never runs the new text. New steps may only be appended —
// -update-migrations refuses to rewrite a line that is already in the golden.
func TestMigrationStepsSnapshot(t *testing.T) {
	t.Parallel()
	got := migrationStepLines()
	raw, err := os.ReadFile(migrationStepsGoldenPath)
	if err != nil && (!errors.Is(err, os.ErrNotExist) || !*updateMigrations) {
		t.Fatalf("read golden (run with -update-migrations to create it): %v", err)
	}
	var want []string
	if trimmed := strings.TrimRight(string(raw), "\n"); trimmed != "" {
		want = strings.Split(trimmed, "\n")
	}

	for i := range want {
		if i >= len(got) {
			t.Fatalf("step %q was removed; shipped steps must stay in place", want[i])
		}
		if got[i] != want[i] {
			t.Fatalf("step changed; shipped steps are append-only.\n got: %q\nwant: %q\n"+
				"Add a new step at the end instead of editing, renaming or moving this one.", got[i], want[i])
		}
	}
	if len(got) == len(want) {
		return
	}
	if !*updateMigrations {
		t.Fatalf("%d new migration step(s) are missing from %s; re-run with -update-migrations and check that the diff only appends lines",
			len(got)-len(want), migrationStepsGoldenPath)
	}
	if err := os.MkdirAll(filepath.Dir(migrationStepsGoldenPath), 0o755); err != nil {
		t.Fatalf("create testdata: %v", err)
	}
	if err := os.WriteFile(migrationStepsGoldenPath, []byte(strings.Join(got, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write golden: %v", err)
	}
}

// TestMigrationSteps_AllRunnable guards against a nil entry sneaking into the
// list — a nil run would panic at server startup, not at compile time.
func TestMigrationSteps_AllRunnable(t *testing.T) {
	t.Parallel()
	for i, step := range migrationSteps {
		if step.run == nil || step.desc == "" {
			t.Fatalf("migrationSteps[%d] needs both a desc and a run func", i)
		}
	}
}

// openBaselineReleaseDB returns an un-initialised store holding the
// baselineRelease fresh schema, after prepare has adjusted it.
func openBaselineReleaseDB(t *testing.T, prepare ...string) *SQLiteStore {
	t.Helper()
	return openRawDB(t, append([]string{v1_5_109Fresh}, prepare...)...)
}

// openLedgerCompleteDB returns an un-initialised store holding a baselineRelease
// database after ledgerCompleteRelease has started on it — the oldest database
// Init accepts — after prepare has adjusted it.
func openLedgerCompleteDB(t *testing.T, prepare ...string) *SQLiteStore {
	t.Helper()
	stmts := append([]string{v1_5_109Fresh}, ledgerCompleteFinish...)
	return openRawDB(t, append(stmts, prepare...)...)
}

func openRawDB(t *testing.T, stmts ...string) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			t.Fatalf("prepare %.60q: %v", stmt, err)
		}
	}
	return s
}

// normalizedSchema describes every table by its column set, foreign keys and
// CHECK clauses, plus every index and trigger definition. Column order is left
// out: upgraded tables carry columns in the order their ALTERs ran.
func normalizedSchema(t *testing.T, s *SQLiteStore) []string {
	t.Helper()
	ctx := context.Background()
	query := func(q string, args ...any) [][]string {
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		defer func() { _ = rows.Close() }()
		cols, _ := rows.Columns()
		var out [][]string
		for rows.Next() {
			vals := make([]sql.NullString, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatalf("scan %s: %v", q, err)
			}
			row := make([]string, len(cols))
			for i, v := range vals {
				row[i] = fmt.Sprintf("%v:%s", v.Valid, v.String)
			}
			out = append(out, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate %s: %v", q, err)
		}
		return out
	}
	checkRe := regexp.MustCompile(`(?i)CHECK\s*\((?:[^()]|\([^()]*\))*\)`)
	var out []string
	for _, row := range query("SELECT type, name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'") {
		kind, name, ddl := strings.TrimPrefix(row[0], "true:"), strings.TrimPrefix(row[1], "true:"), strings.TrimPrefix(row[2], "true:")
		if kind != "table" {
			out = append(out, kind+" "+name+" "+strings.Join(strings.Fields(ddl), " "))
			continue
		}
		for _, col := range query("SELECT name, type, \"notnull\", dflt_value, pk FROM pragma_table_info(?)", name) {
			out = append(out, "column "+name+" "+strings.Join(col, " "))
		}
		for _, fk := range query("SELECT \"table\", \"from\", \"to\", on_update, on_delete FROM pragma_foreign_key_list(?)", name) {
			out = append(out, "fk "+name+" "+strings.Join(fk, " "))
		}
		for _, check := range checkRe.FindAllString(ddl, -1) {
			out = append(out, "check "+name+" "+strings.Join(strings.Fields(check), " "))
		}
		// Covers the autoindexes behind column-level UNIQUE / PRIMARY KEY,
		// which have no sqlite_master SQL of their own.
		for _, idx := range query("SELECT name, \"unique\", origin, partial FROM pragma_index_list(?)", name) {
			var cols []string
			for _, col := range query("SELECT name, coll, \"desc\" FROM pragma_index_xinfo(?) WHERE key = 1 ORDER BY seqno", strings.TrimPrefix(idx[0], "true:")) {
				cols = append(cols, strings.Join(col, " "))
			}
			out = append(out, "index-of "+name+" "+strings.Join(idx[1:], " ")+" ("+strings.Join(cols, ", ")+")")
		}
	}
	sort.Strings(out)
	return out
}

func ledger(t *testing.T, s *SQLiteStore) map[int]appliedMigration {
	t.Helper()
	applied, err := s.appliedMigrations()
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	return applied
}

// A fresh install and an upgrade of a database ledgerCompleteRelease started
// must land on the same schema and the same complete ledger, or the two
// populations drift apart.
func TestInit_FreshDatabaseMatchesUpgradedRelease(t *testing.T) {
	t.Parallel()
	fresh := newTestStore(t)
	upgraded := openLedgerCompleteDB(t)
	if err := upgraded.Init(); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	a, b := normalizedSchema(t, fresh), normalizedSchema(t, upgraded)
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		onlyIn := func(x, y []string) []string {
			seen := make(map[string]bool, len(y))
			for _, line := range y {
				seen[line] = true
			}
			var diff []string
			for _, line := range x {
				if !seen[line] {
					diff = append(diff, line)
				}
			}
			return diff
		}
		t.Fatalf("schemas differ\nonly fresh:\n  %s\nonly upgraded:\n  %s",
			strings.Join(onlyIn(a, b), "\n  "), strings.Join(onlyIn(b, a), "\n  "))
	}
	for name, s := range map[string]*SQLiteStore{"fresh": fresh, "upgraded": upgraded} {
		applied := ledger(t, s)
		for version := 1; version <= baselineVersion; version++ {
			if _, ok := applied[version]; !ok {
				t.Errorf("%s ledger lacks version %d", name, version)
			}
		}
	}
}

// Anything short of a complete 1..baselineVersion ledger is sent back through
// the releases that complete it rather than guessed at: this binary no longer
// carries the steps that would close the gap — including the users cleanup
// that baselineRelease itself left deferred.
func TestInit_RefusesDatabaseOlderThanLedgerCompleteRelease(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		db      func(t *testing.T) *SQLiteStore
		missing string
	}{
		"deferred users cleanup": {func(t *testing.T) *SQLiteStore { return openBaselineReleaseDB(t) }, "94"},
		"skipped step": {func(t *testing.T) *SQLiteStore {
			return openLedgerCompleteDB(t, "DELETE FROM schema_migrations WHERE version = 50")
		}, "50"},
		"older release": {func(t *testing.T) *SQLiteStore {
			return openBaselineReleaseDB(t, "DELETE FROM schema_migrations WHERE version > 81")
		}, "82-108"},
		"pre-ledger database": {func(t *testing.T) *SQLiteStore {
			return openBaselineReleaseDB(t, "DROP TABLE schema_migrations")
		}, "1-108"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := tc.db(t)
			before, err := tableColumns(context.Background(), s.db, "users")
			if err != nil {
				t.Fatalf("users columns: %v", err)
			}
			err = s.Init()
			if err == nil {
				t.Fatal("Init accepted a database older than the baseline")
			}
			for _, want := range []string{
				"missing migration(s) " + tc.missing + ")",
				"daymug upgrade --version " + ledgerCompleteRelease,
				"daymug upgrade --version " + baselineRelease,
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Init error = %q, want it to contain %q", err, want)
				}
			}
			after, err := tableColumns(context.Background(), s.db, "users")
			if err != nil {
				t.Fatalf("users columns: %v", err)
			}
			if len(after) != len(before) {
				t.Errorf("users columns changed despite the refusal: %d -> %d", len(before), len(after))
			}
			for version := range ledger(t, s) {
				if version > baselineVersion {
					t.Errorf("step %d recorded despite the refusal", version)
				}
			}
		})
	}
}

// Rolling back to baselineRelease must find every folded version recorded,
// and flagged destructive so binaries older than it refuse the trimmed schema.
func TestInit_FreshDatabaseStampsWholeBaseline(t *testing.T) {
	t.Parallel()
	applied := ledger(t, newTestStore(t))
	if len(applied) != baselineVersion+len(migrationSteps) {
		t.Fatalf("ledger rows = %d, want %d", len(applied), baselineVersion+len(migrationSteps))
	}
	for version := 1; version <= baselineVersion; version++ {
		if m := applied[version]; !m.destructive {
			t.Fatalf("baseline version %d = %+v, want a destructive entry", version, m)
		}
	}
	if err := assertBinaryCanReadSchema(applied, baselineVersion-1); err == nil {
		t.Fatal("a binary older than the baseline must refuse a fresh database")
	}
}

func TestAssertBinaryCanReadSchema_RejectsDestructiveGapOnly(t *testing.T) {
	t.Parallel()
	next := baselineVersion + 1
	additive := map[int]appliedMigration{next: {name: "add harmless table"}}
	if err := assertBinaryCanReadSchema(additive, baselineVersion); err != nil {
		t.Fatalf("additive gap should remain readable: %v", err)
	}
	destructive := map[int]appliedMigration{next: {name: "drop old column", destructive: true}}
	if err := assertBinaryCanReadSchema(destructive, baselineVersion); err == nil {
		t.Fatal("destructive schema gap must reject the older binary")
	}
}

// A failure while recording a step must roll the step back too, so the next
// startup applies it exactly once.
func TestApplyMigration_StepAndLedgerRowCommitTogether(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	const version = 10_000
	step := migrationStep{desc: "create probe", run: func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "CREATE TABLE probe (id INTEGER)")
		return err
	}}
	if _, err := s.db.Exec(fmt.Sprintf(`
CREATE TRIGGER fail_ledger BEFORE INSERT ON schema_migrations
WHEN NEW.version = %d
BEGIN SELECT RAISE(ABORT, 'simulated crash before ledger row'); END`, version)); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	err := s.applyMigration(ctx, version, step)
	if err == nil || !strings.Contains(err.Error(), "simulated crash before ledger row") {
		t.Fatalf("applyMigration error = %v, want the simulated ledger failure", err)
	}
	assertCount(t, s, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'probe'", 0)

	if _, err := s.db.Exec("DROP TRIGGER fail_ledger"); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if err := s.applyMigration(ctx, version, step); err != nil {
		t.Fatalf("applyMigration after recovery: %v", err)
	}
	assertCount(t, s, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'probe'", 1)
	assertCount(t, s, fmt.Sprintf("SELECT COUNT(*) FROM schema_migrations WHERE version = %d AND name = 'create probe'", version), 1)
}

func foreignKeysEnabled(t *testing.T, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) int {
	t.Helper()
	var on int
	if err := q.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&on); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	return on
}

// A table-rebuild step must see enforcement off on its own connection —
// issuing the PRAGMA on whichever pooled connection came first is not enough —
// and the connection must go back to the pool with enforcement on again.
func TestWithForeignKeysOffTx_ScopesPragmaToTheTransaction(t *testing.T) {
	t.Parallel()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "fk.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.db.SetMaxOpenConns(1) // every later query reuses the connection the step ran on

	ctx := context.Background()
	if err := s.withForeignKeysOffTx(ctx, func(tx *sql.Tx) error {
		if on := foreignKeysEnabled(t, tx); on != 0 {
			t.Errorf("foreign_keys inside step = %d, want 0", on)
		}
		return nil
	}); err != nil {
		t.Fatalf("withForeignKeysOffTx: %v", err)
	}
	if on := foreignKeysEnabled(t, s.db); on != 1 {
		t.Fatalf("foreign_keys after step = %d, want 1", on)
	}
}

func TestFormatVersionRanges(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   []int
		want string
	}{
		{nil, ""},
		{[]int{94}, "94"},
		{[]int{1, 2, 3, 94}, "1-3, 94"},
		{[]int{50, 94, 95, 96}, "50, 94-96"},
	} {
		if got := formatVersionRanges(tc.in); got != tc.want {
			t.Errorf("formatVersionRanges(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
