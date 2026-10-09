package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
)

const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version      INTEGER PRIMARY KEY,
    name         TEXT NOT NULL DEFAULT '',
    destructive INTEGER NOT NULL DEFAULT 0,
    applied_at   DATETIME NOT NULL DEFAULT (datetime('now'))
);`

const (
	// baselineVersion is the last ledger version folded into baselineSchema.
	// Versions 1..baselineVersion keep their historical meaning in every
	// existing ledger, so they are never reused: new steps start after it.
	baselineVersion = 108
	// baselineRelease is the last release that carried the individual steps.
	// A database it has not brought up to baselineVersion is sent back
	// through it.
	baselineRelease = "v1.5.109"
	// ledgerCompleteRelease is the first release that records every version
	// up to baselineVersion: baselineRelease itself left one step (94, the
	// trim of the Agent-era users columns) deferred, and only this release
	// finishes it. This binary accepts nothing older.
	ledgerCompleteRelease = "v0.0.1"
	baselineMigrationName = "baseline schema"
)

// migrationStep is one schema change after the baseline. Steps run in slice
// order and are recorded as version baselineVersion+index+1, so a step must
// never be removed or reordered once released — append instead.
type migrationStep struct {
	// desc names the step in the ledger and in the error returned by Init.
	desc string
	// destructive marks steps that remove or rewrite schema older binaries read.
	destructive bool
	// foreignKeysOff runs the step with foreign-key enforcement disabled.
	// PRAGMA foreign_keys is a silent no-op inside a transaction, so such a
	// step gets a dedicated connection that has enforcement switched off
	// before BEGIN and back on after the transaction ends.
	foreignKeysOff bool
	// run performs the step inside the transaction that also records it in
	// schema_migrations. It must use only tx: see withTx.
	run func(ctx context.Context, tx *sql.Tx) error
}

// migrationSteps are the migrations added since the baseline.
var migrationSteps = []migrationStep{
	// 109
	{
		desc: "convert legacy JSON env values to VAR=VAL lines",
		run:  convertLegacyJSONEnv,
	},
	// 110
	{
		desc: "delete bot_threads rows with a bare platform",
		run:  deleteBareBotThreads,
	},
	// 111
	{
		desc:        "drop users.archived_at, conversations.archived, agents.permission_rules, agents.source",
		destructive: true,
		run:         dropDeadColumns,
	},
	// 112
	{
		desc:        "fold agents.skills into role_definition and drop the column",
		destructive: true,
		run:         foldAgentSkills,
	},
	// 113
	{
		desc:        "drop the external HTTP API: api_tokens, api_session_costs, users.allow_api",
		destructive: true,
		run:         dropExternalAPI,
	},
	// 114
	{
		desc:        "drop published sites: served_directories, users.serve_slug, agents.serve_slug",
		destructive: true,
		run:         dropPublishedSites,
	},
}

type appliedMigration struct {
	name        string
	destructive bool
}

// Init brings the database to the current schema: a fresh one is created from
// baselineSchema, and then every pending post-baseline step runs exactly once.
func (s *SQLiteStore) Init() error {
	ctx := context.Background()
	if err := s.ensureSchemaMigrationsTable(); err != nil {
		return err
	}
	applied, err := s.appliedMigrations()
	if err != nil {
		return err
	}
	if err := assertBinaryCanReadSchema(applied, baselineVersion+len(migrationSteps)); err != nil {
		return err
	}
	if err := s.ensureBaseline(ctx, applied); err != nil {
		return err
	}
	for i, step := range migrationSteps {
		version := baselineVersion + i + 1
		if _, ok := applied[version]; ok {
			continue
		}
		if err := s.applyMigration(ctx, version, step); err != nil {
			return err
		}
	}
	s.startMaintenance()
	return nil
}

func (s *SQLiteStore) ensureBaseline(ctx context.Context, applied map[int]appliedMigration) error {
	var missing []int
	for version := 1; version <= baselineVersion; version++ {
		if _, ok := applied[version]; !ok {
			missing = append(missing, version)
		}
	}
	switch {
	case len(missing) == 0:
		return nil
	case len(missing) == baselineVersion:
		// An empty ledger is either a new database or one from before the
		// ledger existed; only the former has no tables yet.
		existing, err := s.tableExists("users")
		if err != nil {
			return err
		}
		if !existing {
			return s.withTx(ctx, func(tx *sql.Tx) error { return createBaseline(ctx, tx) })
		}
	}
	return fmt.Errorf(
		"database schema predates DayMug %s (missing migration(s) %s); upgrade to it first with `daymug upgrade --version %s` (a database older than %s goes through `daymug upgrade --version %s` before that), let it start once, then upgrade again",
		ledgerCompleteRelease, formatVersionRanges(missing), ledgerCompleteRelease, baselineRelease, baselineRelease)
}

// createBaseline stamps every folded version, not just one marker: a rollback
// to baselineRelease must see the whole range as applied, or it would replay
// the old steps — some of them not idempotent — on top of this schema. The
// rows are flagged destructive so a binary older than the baseline refuses
// the trimmed schema instead of misreading it.
func createBaseline(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, baselineSchema); err != nil {
		return fmt.Errorf("create baseline schema: %w", err)
	}
	for version := 1; version <= baselineVersion; version++ {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, name, destructive) VALUES (?, ?, 1)",
			version, baselineMigrationName,
		); err != nil {
			return fmt.Errorf("stamp baseline %d: %w", version, err)
		}
	}
	return nil
}

// applyMigration runs one step and records it in a single transaction, so
// the step is either fully applied and recorded or not applied at all.
func (s *SQLiteStore) applyMigration(ctx context.Context, version int, step migrationStep) error {
	apply := func(tx *sql.Tx) error {
		if err := step.run(ctx, tx); err != nil {
			return fmt.Errorf("%s: %w", step.desc, err)
		}
		return recordMigration(ctx, tx, version, step)
	}
	if step.foreignKeysOff {
		return s.withForeignKeysOffTx(ctx, apply)
	}
	return s.withTx(ctx, apply)
}

func (s *SQLiteStore) ensureSchemaMigrationsTable() error {
	if _, err := s.db.Exec(schemaMigrationsDDL); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}

func (s *SQLiteStore) appliedMigrations() (map[int]appliedMigration, error) {
	rows, err := s.db.Query("SELECT version, name, destructive FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int]appliedMigration)
	for rows.Next() {
		var version, destructive int
		var name string
		if err := rows.Scan(&version, &name, &destructive); err != nil {
			return nil, fmt.Errorf("scan schema_migrations row: %w", err)
		}
		applied[version] = appliedMigration{name: name, destructive: destructive != 0}
	}
	return applied, rows.Err()
}

func assertBinaryCanReadSchema(applied map[int]appliedMigration, supported int) error {
	var ahead []int
	destructive := make(map[int]string)
	for version, migration := range applied {
		if version <= supported {
			continue
		}
		ahead = append(ahead, version)
		if migration.destructive {
			destructive[version] = migration.name
		}
	}
	if len(ahead) == 0 {
		return nil
	}
	sort.Ints(ahead)
	highest := ahead[len(ahead)-1]
	if len(destructive) == 0 {
		log.Printf("[store] database schema is at version %d but this binary only knows %d; newer steps are additive, so startup continues", highest, supported)
		return nil
	}
	var names []string
	for _, version := range ahead {
		if name, ok := destructive[version]; ok {
			names = append(names, fmt.Sprintf("#%d %q", version, name))
		}
	}
	return fmt.Errorf(
		"database schema is at version %d, this binary only supports %d, and the gap contains destructive migration(s) [%s]; reinstall the newer DayMug binary or restore the pre-upgrade database backup",
		highest, supported, strings.Join(names, ", "))
}

// recordMigration marks version as applied. It runs on the step's own
// transaction, so a crash can never leave a step's effects committed without
// the ledger row that stops the next startup from replaying it.
func recordMigration(ctx context.Context, tx *sql.Tx, version int, step migrationStep) error {
	destructive := 0
	if step.destructive {
		destructive = 1
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations (version, name, destructive) VALUES (?, ?, ?)",
		version, step.desc, destructive,
	); err != nil {
		return fmt.Errorf("record migration %d: %w", version, err)
	}
	return nil
}

// formatVersionRanges renders sorted versions compactly ("1-81, 94").
func formatVersionRanges(versions []int) string {
	var parts []string
	for i := 0; i < len(versions); {
		j := i
		for j+1 < len(versions) && versions[j+1] == versions[j]+1 {
			j++
		}
		if i == j {
			parts = append(parts, fmt.Sprint(versions[i]))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", versions[i], versions[j]))
		}
		i = j + 1
	}
	return strings.Join(parts, ", ")
}

func (s *SQLiteStore) tableExists(name string) (bool, error) {
	var found string
	err := s.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name = ?", name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("look up table %s: %w", name, err)
	}
	return true, nil
}

// queryer is the read side shared by *sql.DB and *sql.Tx, so schema probes
// can run either against the pool or inside a migration step's transaction.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func tableColumns(ctx context.Context, q queryer, table string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, fmt.Errorf("pragma table_info(%s): %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("scan %s pragma row: %w", table, err)
		}
		columns[name] = true
	}
	return columns, rows.Err()
}
