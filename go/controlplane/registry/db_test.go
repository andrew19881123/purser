package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDBConfigFromEnv_DefaultSQLite(t *testing.T) {
	t.Setenv("PURSER_DB_DRIVER", "")
	t.Setenv("PURSER_DB", "")
	cfg := DBConfigFromEnv()
	assert.Equal(t, "sqlite", cfg.Driver)
	assert.Equal(t, "/data/purser-registry.db", cfg.DSN)
}

func TestDBConfigFromEnv_SQLiteCustomPath(t *testing.T) {
	t.Setenv("PURSER_DB_DRIVER", "sqlite")
	t.Setenv("PURSER_DB", "/tmp/test.db")
	cfg := DBConfigFromEnv()
	assert.Equal(t, "sqlite", cfg.Driver)
	assert.Equal(t, "/tmp/test.db", cfg.DSN)
}

func TestDBConfigFromEnv_Postgres(t *testing.T) {
	t.Setenv("PURSER_DB_DRIVER", "postgres")
	t.Setenv("PURSER_DB_URL", "postgres://user:pass@localhost/db")
	cfg := DBConfigFromEnv()
	assert.Equal(t, "postgres", cfg.Driver)
	assert.Equal(t, "postgres://user:pass@localhost/db", cfg.DSN)
}

func TestDBConfigFromEnv_PostgresAlias(t *testing.T) {
	// "postgresql" is normalized to "postgres"
	t.Setenv("PURSER_DB_DRIVER", "postgresql")
	t.Setenv("PURSER_DB_URL", "postgres://a:b@localhost/mydb")
	cfg := DBConfigFromEnv()
	assert.Equal(t, "postgres", cfg.Driver)
}

func TestDBConfigFromEnv_PostgresDefaultDSN(t *testing.T) {
	t.Setenv("PURSER_DB_DRIVER", "postgres")
	t.Setenv("PURSER_DB_URL", "")
	cfg := DBConfigFromEnv()
	assert.Equal(t, "postgres", cfg.Driver)
	assert.Contains(t, cfg.DSN, "localhost:5432")
}

func TestOpenDB_SQLiteInMemory(t *testing.T) {
	db, err := OpenDB(DBConfig{Driver: "sqlite", DSN: ":memory:"})
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.Ping())
}

func TestOpenDB_UnknownDriver_ReturnsError(t *testing.T) {
	_, err := OpenDB(DBConfig{Driver: "mysql", DSN: "..."})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported DB driver")
}
