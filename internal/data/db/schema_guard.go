package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/colonyops/hive/internal/data/migrate"
)

// ErrSchemaAhead marks a write refused because a newer build migrated hive.db.
var ErrSchemaAhead = errors.New("hive.db schema is newer than this build")

// SchemaAheadError is the error a write returns when a newer build migrated
// hive.db. It matches ErrSchemaAhead.
type SchemaAheadError struct {
	// Binary is the program to upgrade, from OpenOptions.Binary.
	Binary        string
	DBVersion     int
	BinaryVersion int
	// Err is the SQLite error. It is nil when the value is built for display.
	Err error
}

func (e *SchemaAheadError) Error() string {
	return fmt.Sprintf("hive.db is at schema %d, this %s supports %d; upgrade %s to write",
		e.DBVersion, e.Binary, e.BinaryVersion, e.Binary)
}

func (e *SchemaAheadError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrSchemaAhead}
	}
	return []error{ErrSchemaAhead, e.Err}
}

type schemaGuard struct {
	binary        string
	dbVersion     int
	binaryVersion int
}

func (g schemaGuard) ahead() bool {
	return g.dbVersion > g.binaryVersion
}

func (g schemaGuard) translate(err error) error {
	if !g.ahead() {
		return err
	}

	var sqliteErr *sqlite.Error
	// The low byte is the primary result code. Extended codes such as
	// SQLITE_READONLY_DBMOVED keep it.
	if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_READONLY {
		return err
	}

	return &SchemaAheadError{
		Binary:        g.binary,
		DBVersion:     g.dbVersion,
		BinaryVersion: g.binaryVersion,
		Err:           err,
	}
}

// guardedConn is the DBTX that Queries use on a read-only database.
type guardedConn struct {
	conn  *sql.DB
	guard schemaGuard
}

func (c *guardedConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := c.conn.ExecContext(ctx, query, args...)
	return result, c.guard.translate(err)
}

func (c *guardedConn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := c.conn.QueryContext(ctx, query, args...)
	return rows, c.guard.translate(err)
}

func (c *guardedConn) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return c.conn.PrepareContext(ctx, query)
}

// QueryRowContext cannot translate the error: *sql.Row holds it until Scan.
// A caller that writes through a single-row query uses DB.WriteError.
func (c *guardedConn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return c.conn.QueryRowContext(ctx, query, args...)
}

func latestVersion(migrations []migrate.Migration) int {
	if len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].Version
}

func maxVersion(applied map[int]bool) int {
	highest := 0
	for version := range applied {
		highest = max(highest, version)
	}
	return highest
}
