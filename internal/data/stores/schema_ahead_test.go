package stores

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/colonyops/hive/internal/core/hc"
	"github.com/colonyops/hive/internal/core/notify"
	"github.com/colonyops/hive/internal/data/db"
)

func openSchemaAheadDB(t *testing.T) *db.DB {
	t.Helper()
	dataDir := t.TempDir()

	database, err := db.Open(dataDir, db.DefaultOpenOptions())
	require.NoError(t, err)
	dbVersion, _ := database.SchemaVersions()
	_, err = database.Conn().ExecContext(context.Background(),
		"INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, 'future', 0)", dbVersion+1)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	database, err = db.Open(dataDir, db.DefaultOpenOptions())
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	require.True(t, database.ReadOnly())

	return database
}

// These stores write through a query that returns one row, so the DB cannot
// translate the error for them.
func TestSingleRowWrites_SchemaAhead(t *testing.T) {
	ctx := context.Background()
	database := openSchemaAheadDB(t)

	t.Run("hc comment", func(t *testing.T) {
		err := NewHCStore(database).AddComment(ctx, hc.Comment{
			ID:        "hcc-1",
			ItemID:    "hc-1",
			Message:   "m",
			CreatedAt: time.Now(),
		})
		require.ErrorIs(t, err, db.ErrSchemaAhead)
	})

	t.Run("notification", func(t *testing.T) {
		_, err := NewNotifyStore(database).Save(ctx, notify.Notification{
			Level:     notify.LevelInfo,
			Message:   "m",
			CreatedAt: time.Now(),
		})
		require.ErrorIs(t, err, db.ErrSchemaAhead)
	})
}
