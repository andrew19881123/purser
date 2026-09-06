package backup_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/purser/purser/go/controlplane/backup"
	_ "modernc.org/sqlite"
)

// seedDB creates a SQLite database at path, creates a table, inserts a row,
// and closes the connection.
func seedDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("seed: open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t (id INTEGER)"); err != nil {
		t.Fatalf("seed: create table: %v", err)
	}
	if _, err := db.Exec("INSERT INTO t VALUES (42)"); err != nil {
		t.Fatalf("seed: insert: %v", err)
	}
}

// queryInt runs a single-value integer SELECT against the SQLite file at path.
func queryInt(t *testing.T, path, query string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("queryInt: open: %v", err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow(query).Scan(&v); err != nil {
		t.Fatalf("queryInt %q: %v", query, err)
	}
	return v
}

// TestBackupCreatesValidSQLite is the primary happy-path test: BackupDB must
// produce a file that is a valid SQLite database containing the source data.
func TestBackupCreatesValidSQLite(t *testing.T) {
	srcDB := filepath.Join(t.TempDir(), "src.db")
	dstDB := filepath.Join(t.TempDir(), "backup.db")

	seedDB(t, srcDB)

	if err := backup.BackupDB(srcDB, dstDB); err != nil {
		t.Fatalf("BackupDB: %v", err)
	}

	got := queryInt(t, dstDB, "SELECT id FROM t")
	if got != 42 {
		t.Errorf("backup row value = %d, want 42", got)
	}
}

// TestBackupFailsIfDestinationExists verifies that BackupDB refuses to
// overwrite an existing file at the destination path.
func TestBackupFailsIfDestinationExists(t *testing.T) {
	srcDB := filepath.Join(t.TempDir(), "src.db")
	dstDB := filepath.Join(t.TempDir(), "existing.db")

	seedDB(t, srcDB)
	seedDB(t, dstDB) // destination already exists

	if err := backup.BackupDB(srcDB, dstDB); err == nil {
		t.Fatal("BackupDB with existing destination: want error, got nil")
	}
}

// TestRestoreReplacesDB verifies that RestoreDB atomically replaces the
// destination with the backup and that the restored data is readable.
func TestRestoreReplacesDB(t *testing.T) {
	backupFile := filepath.Join(t.TempDir(), "backup.db")
	dstDB := filepath.Join(t.TempDir(), "dst.db")

	seedDB(t, backupFile)

	// The destination does not need to exist before a restore.
	if err := backup.RestoreDB(backupFile, dstDB); err != nil {
		t.Fatalf("RestoreDB: %v", err)
	}

	got := queryInt(t, dstDB, "SELECT id FROM t")
	if got != 42 {
		t.Errorf("restored row value = %d, want 42", got)
	}
}

// TestRestoreRejectsNonSQLite verifies that RestoreDB rejects a file that does
// not carry the SQLite 3 magic header before touching the destination.
func TestRestoreRejectsNonSQLite(t *testing.T) {
	notDB := filepath.Join(t.TempDir(), "not-a-db.bin")
	dstDB := filepath.Join(t.TempDir(), "dst.db")

	if err := os.WriteFile(notDB, []byte("this is definitely not sqlite"), 0o600); err != nil {
		t.Fatalf("write fake file: %v", err)
	}

	if err := backup.RestoreDB(notDB, dstDB); err == nil {
		t.Fatal("RestoreDB with non-SQLite input: want error, got nil")
	}
}

// TestRestoreDoesNotCorruptExistingOnError verifies that a failed restore
// (missing source) leaves the original destination untouched.
func TestRestoreDoesNotCorruptExistingOnError(t *testing.T) {
	dstDB := filepath.Join(t.TempDir(), "dst.db")
	seedDB(t, dstDB)

	// Attempt restore from a path that does not exist.
	if err := backup.RestoreDB("/nonexistent/no-such.db", dstDB); err == nil {
		t.Fatal("RestoreDB with missing source: want error, got nil")
	}

	// The original destination must still be intact and contain the seeded data.
	got := queryInt(t, dstDB, "SELECT id FROM t")
	if got != 42 {
		t.Errorf("dst row after failed restore = %d, want 42 (destination was corrupted)", got)
	}
}
