package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// SnapshotDatabase writes a transactionally consistent copy of the SQLite
// database at srcPath to dstPath using VACUUM INTO. Unlike a file copy it
// folds in any WAL frames left by an unclean shutdown, so the result is a
// single self-contained file. dstPath must not exist.
//
// It opens srcPath without running migrations, so an older binary (the
// upgrade watchdog) can snapshot a database a newer one has touched.
func SnapshotDatabase(ctx context.Context, srcPath, dstPath string) error {
	if _, err := os.Stat(srcPath); err != nil {
		return fmt.Errorf("snapshot source: %w", err)
	}
	if _, err := os.Stat(dstPath); err == nil {
		return fmt.Errorf("snapshot destination %s already exists", dstPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("snapshot destination: %w", err)
	}
	db, err := sql.Open("sqlite", dsnWithPragmas(srcPath))
	if err != nil {
		return fmt.Errorf("open %s: %w", srcPath, err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", dstPath); err != nil {
		_ = os.Remove(dstPath)
		return fmt.Errorf("vacuum into %s: %w", dstPath, err)
	}
	return nil
}
