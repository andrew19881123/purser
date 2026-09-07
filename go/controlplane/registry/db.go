package registry

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver for database/sql
	_ "modernc.org/sqlite"             // SQLite driver
)

// DBConfig holds the database connection configuration.
type DBConfig struct {
	// Driver is "sqlite" (default, dev/test only) or "postgres" (production).
	Driver string
	// DSN is the data source name. For SQLite: file path or ":memory:".
	// For PostgreSQL: "postgres://user:pass@host:5432/dbname?sslmode=require"
	DSN string
}

// DBConfigFromEnv reads DB config from environment variables:
//
//	PURSER_DB_DRIVER — "sqlite" or "postgres" (default: "sqlite")
//	PURSER_DB_URL    — PostgreSQL DSN (required when driver=postgres)
//	PURSER_DB        — SQLite file path (default: /data/purser-registry.db)
func DBConfigFromEnv() DBConfig {
	driver := os.Getenv("PURSER_DB_DRIVER")
	if driver == "" {
		driver = "sqlite"
	}
	var dsn string
	switch driver {
	case "postgres", "postgresql":
		driver = "postgres"
		dsn = os.Getenv("PURSER_DB_URL")
		if dsn == "" {
			dsn = "postgres://purser:purser@localhost:5432/purser?sslmode=disable"
		}
	default:
		driver = "sqlite"
		dsn = os.Getenv("PURSER_DB")
		if dsn == "" {
			dsn = "/data/purser-registry.db"
		}
	}
	return DBConfig{Driver: driver, DSN: dsn}
}

// OpenDB opens a database connection using the given config.
// For SQLite it adds WAL mode, busy timeout, and foreign keys pragmas.
// For PostgreSQL it uses pgx driver with standard connection pool settings.
func OpenDB(cfg DBConfig) (*sql.DB, error) {
	switch cfg.Driver {
	case "postgres":
		db, err := sql.Open("pgx", cfg.DSN)
		if err != nil {
			return nil, fmt.Errorf("open postgres: %w", err)
		}
		// Standard production connection pool.
		db.SetMaxOpenConns(25)
		db.SetMaxIdleConns(10)
		return db, nil

	case "sqlite":
		dsn := fmt.Sprintf(
			"file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"+
				"&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)",
			cfg.DSN,
		)
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
		}
		// SQLite is single-writer; no connection pool needed.
		db.SetMaxOpenConns(1)
		return db, nil

	default:
		return nil, fmt.Errorf("unsupported DB driver %q (use 'sqlite' or 'postgres')", cfg.Driver)
	}
}
