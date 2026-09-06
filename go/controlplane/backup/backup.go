// Package backup provides SQLite online backup and restore primitives for the
// Purser control plane registry.
//
// Backup uses SQLite's VACUUM INTO statement to produce a consistent, fully
// vacuumed copy of the source database without acquiring a write lock or
// pausing concurrent readers/writers.
//
// Restore copies the backup file to a temp path in the same directory as the
// destination, verifies the SQLite magic header, then atomically renames it
// into place. The control plane process must not be running when a restore is
// applied — stop it first, restore, then restart.
package backup

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	// Import the same pure-Go SQLite driver used by the registry so that
	// "sqlite" is registered in the global database/sql driver list.
	_ "modernc.org/sqlite"
)

// sqliteMagic is the 16-byte header that every SQLite 3 database starts with.
const sqliteMagic = "SQLite format 3\x00"

// BackupDB creates a consistent online backup of the SQLite database at
// srcPath and writes it to dstPath using VACUUM INTO.
//
// VACUUM INTO is safe to run while the source database is open by another
// process: it acquires only a shared read lock for the duration of the copy,
// so ongoing writes are not blocked. The destination file must not exist
// before the call; BackupDB returns an error if it does.
func BackupDB(srcPath, dstPath string) error {
	if _, err := os.Stat(dstPath); err == nil {
		return fmt.Errorf("backup: destination already exists: %s (remove it first or choose a different path)", dstPath)
	}

	// Open with the same pragmas used by the registry so the backup connection
	// sees WAL-mode pages correctly.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)",
		srcPath,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("backup: open source: %w", err)
	}
	defer db.Close()

	if _, err := db.Exec("VACUUM INTO ?", dstPath); err != nil {
		// Clean up any partial file the driver may have created.
		_ = os.Remove(dstPath)
		return fmt.Errorf("backup: vacuum into: %w", err)
	}
	return nil
}

// RestoreDB replaces the database at dstPath with the backup at srcPath.
//
// RestoreDB verifies that srcPath carries the SQLite 3 magic header, copies
// it to a temporary file in the same directory as dstPath, then atomically
// renames it into place. The atomic rename means dstPath is never left in a
// partially-written state.
//
// The control plane process must not be running against dstPath when
// RestoreDB is called. Stop the service first, run restore, then restart.
func RestoreDB(srcPath, dstPath string) error {
	if err := verifySQLiteHeader(srcPath); err != nil {
		return fmt.Errorf("restore: %w", err)
	}

	// Create the destination directory if it does not yet exist (first-time
	// restore into a fresh installation).
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o750); err != nil {
		return fmt.Errorf("restore: create destination dir: %w", err)
	}

	// Write to a temp file in the same directory so os.Rename is an atomic
	// same-filesystem move — no cross-device copy + delete race.
	tmp, err := os.CreateTemp(filepath.Dir(dstPath), ".purser-restore-*.db")
	if err != nil {
		return fmt.Errorf("restore: create temp file: %w", err)
	}
	tmpName := tmp.Name()

	// Always try to remove the temp file; harmless after a successful Rename.
	defer os.Remove(tmpName)

	src, err := os.Open(srcPath)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("restore: open backup: %w", err)
	}
	defer src.Close()

	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return fmt.Errorf("restore: copy: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("restore: flush temp file: %w", err)
	}

	if err := os.Rename(tmpName, dstPath); err != nil {
		return fmt.Errorf("restore: rename: %w", err)
	}
	return nil
}

// verifySQLiteHeader opens path and confirms it starts with the SQLite 3
// magic string. Returns a descriptive error if the check fails.
func verifySQLiteHeader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("invalid backup file: %w", err)
	}
	defer f.Close()

	buf := make([]byte, len(sqliteMagic))
	if _, err := io.ReadFull(f, buf); err != nil {
		return fmt.Errorf("invalid backup file: read header: %w", err)
	}
	if string(buf) != sqliteMagic {
		return errors.New("invalid backup file: not a SQLite 3 database")
	}
	return nil
}
