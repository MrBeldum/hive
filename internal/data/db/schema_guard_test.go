package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"
)

const desktopBinary = "Hive Desktop"

// seedSchemaAhead returns a data directory whose hive.db carries one
// migration that this build does not know.
func seedSchemaAhead(t *testing.T) (dataDir string, latest int) {
	t.Helper()
	dataDir = t.TempDir()

	database, err := Open(dataDir, DefaultOpenOptions())
	require.NoError(t, err)
	_, latest = database.SchemaVersions()

	_, err = database.Conn().ExecContext(context.Background(),
		"INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, 'future', 0)", latest+1)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	return dataDir, latest
}

func openSchemaAhead(t *testing.T) (database *DB, latest int) {
	t.Helper()
	dataDir, latest := seedSchemaAhead(t)

	opts := DefaultOpenOptions()
	opts.Binary = desktopBinary
	database, err := Open(dataDir, opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	return database, latest
}

func setKV(ctx context.Context, q *Queries) error {
	now := time.Now().UnixNano()
	return q.KVSet(ctx, KVSetParams{Key: "k", Value: []byte("v"), CreatedAt: now, UpdatedAt: now})
}

func requireSchemaAhead(t *testing.T, err error, latest int) {
	t.Helper()
	require.ErrorIs(t, err, ErrSchemaAhead)

	var ahead *SchemaAheadError
	require.ErrorAs(t, err, &ahead)
	assert.Equal(t, desktopBinary, ahead.Binary)
	assert.Equal(t, latest+1, ahead.DBVersion)
	assert.Equal(t, latest, ahead.BinaryVersion)
	assert.Contains(t, err.Error(), "upgrade Hive Desktop to write")

	var sqliteErr *sqlite.Error
	assert.ErrorAs(t, err, &sqliteErr, "the SQLite error stays in the chain")
}

func TestOpen_SchemaAtLatest(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	latest := latestVersion(hiveMigrations(t))

	for _, name := range []string{"fresh database", "second open"} {
		t.Run(name, func(t *testing.T) {
			database, err := Open(dataDir, DefaultOpenOptions())
			require.NoError(t, err)
			defer func() { _ = database.Close() }()

			assert.False(t, database.ReadOnly())
			dbVersion, binaryVersion := database.SchemaVersions()
			assert.Equal(t, latest, dbVersion)
			assert.Equal(t, latest, binaryVersion)

			require.NoError(t, setKV(ctx, database.Queries()))
		})
	}
}

func TestOpen_SchemaAheadIsReadOnly(t *testing.T) {
	ctx := context.Background()
	database, latest := openSchemaAhead(t)

	assert.True(t, database.ReadOnly())
	dbVersion, binaryVersion := database.SchemaVersions()
	assert.Equal(t, latest+1, dbVersion)
	assert.Equal(t, latest, binaryVersion)

	_, err := database.Queries().ListSessions(ctx)
	require.NoError(t, err, "reads work on a newer schema")

	requireSchemaAhead(t, setKV(ctx, database.Queries()), latest)
}

func TestOpen_SchemaAheadAppliesNothing(t *testing.T) {
	ctx := context.Background()
	dataDir, latest := seedSchemaAhead(t)

	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "hive.db"))
	require.NoError(t, err)
	defer func() { _ = raw.Close() }()
	_, err = raw.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = ?", latest)
	require.NoError(t, err)

	database, err := Open(dataDir, DefaultOpenOptions())
	require.NoError(t, err)
	defer func() { _ = database.Close() }()

	var count int
	err = raw.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", latest).Scan(&count)
	require.NoError(t, err)
	assert.Zero(t, count, "Open must not apply a migration to a newer database")
}

func TestOpen_SchemaAheadWithOpenWriter(t *testing.T) {
	ctx := context.Background()
	dataDir, _ := seedSchemaAhead(t)

	writer, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_txlock=immediate", filepath.Join(dataDir, "hive.db")))
	require.NoError(t, err)
	defer func() { _ = writer.Close() }()

	tx, err := writer.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, "INSERT INTO kv_store (key, value, created_at, updated_at) VALUES ('held', x'00', 0, 0)")
	require.NoError(t, err)

	opts := DefaultOpenOptions()
	opts.BusyTimeout = 200
	database, err := Open(dataDir, opts)
	require.NoError(t, err, "Open must not wait for the write lock")
	defer func() { _ = database.Close() }()

	assert.True(t, database.ReadOnly())
}

func TestWithTx_SchemaAhead(t *testing.T) {
	ctx := context.Background()
	database, latest := openSchemaAhead(t)

	err := database.WithTx(ctx, func(q *Queries) error {
		if err := setKV(ctx, q); err != nil {
			return fmt.Errorf("set kv: %w", err)
		}
		return nil
	})

	requireSchemaAhead(t, err, latest)
}

func TestWriteError_SingleRowQuery(t *testing.T) {
	ctx := context.Background()
	database, latest := openSchemaAhead(t)

	_, err := database.Queries().InsertNotification(ctx, InsertNotificationParams{
		Level:     "info",
		Message:   "m",
		CreatedAt: time.Now().UnixNano(),
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrSchemaAhead, "Scan returns the SQLite error untranslated")

	requireSchemaAhead(t, database.WriteError(err), latest)
}

func TestWriteError_WritableDatabase(t *testing.T) {
	database := openTestDB(t)

	err := fmt.Errorf("some failure")
	assert.Same(t, err, database.WriteError(err))
	assert.NoError(t, database.WriteError(nil))
}
