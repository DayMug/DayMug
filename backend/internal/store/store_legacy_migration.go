package store

import (
	"context"
	"database/sql"
	"fmt"
)

// trimLegacyUserAgentColumns finishes historical migration 94. All other
// baseline steps must already be recorded before this function is called.
// Never discard a legacy Agent's configuration before its row has been moved
// out of users. The column changes and ledger entry share applyMigration's
// transaction, so a failed cleanup is safe to retry on the next startup.
func trimLegacyUserAgentColumns(ctx context.Context, tx *sql.Tx) error {
	var remaining int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE username = ''").Scan(&remaining); err != nil {
		return fmt.Errorf("check legacy Agent rows: %w", err)
	}
	if remaining != 0 {
		return fmt.Errorf("%d legacy Agent row(s) remain in users; migrate them to agents before upgrading", remaining)
	}
	columns, err := tableColumns(ctx, tx, "users")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DROP TRIGGER IF EXISTS disable_cron_jobs_for_archived_legacy_agent"); err != nil {
		return err
	}
	for _, column := range []string{
		"skills", "role_definition", "permission_rules", "mcp_config",
		"claude_md_content", "manage_claude_md", "owner_id", "source",
		"allow_cli_mode", "allow_tty_mode",
	} {
		// Older attempts may have already removed a column without stamping 94.
		if !columns[column] {
			continue
		}
		if _, err := tx.ExecContext(ctx, "ALTER TABLE users DROP COLUMN "+column); err != nil {
			return fmt.Errorf("drop users.%s: %w", column, err)
		}
	}
	return nil
}
