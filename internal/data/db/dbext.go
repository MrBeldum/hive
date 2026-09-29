package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
)

// OpenOptions configures database connection settings.
type OpenOptions struct {
	MaxOpenConns int    // max open connections (default: 2)
	MaxIdleConns int    // max idle connections (default: 2)
	BusyTimeout  int    // busy timeout in milliseconds (default: 5000)
	Binary       string // program that SchemaAheadError tells the user to upgrade (default: "hive")
}

// DefaultOpenOptions returns the recommended defaults for SQLite.
func DefaultOpenOptions() OpenOptions {
	return OpenOptions{
		MaxOpenConns: 2,
		MaxIdleConns: 2,
		BusyTimeout:  5000,
		Binary:       "hive",
	}
}

// DB wraps a SQL database connection with sqlc queries.
type DB struct {
	conn    *sql.DB
	queries *Queries
	schema  schemaGuard
}

// Open creates a new database connection with the given options.
// The database file is created in the specified data directory.
// Uses minimal connection pool (default 2) to prevent transaction deadlocks while
// avoiding unnecessary connection overhead. WAL mode and busy_timeout
// handle concurrent access at the SQLite level.
//
// When a newer build migrated the database, Open applies no migration and
// returns a read-only DB. Older builds can read a newer schema because
// migrations are additive. Writes return *SchemaAheadError.
func Open(dataDir string, opts OpenOptions) (*DB, error) {
	// Apply defaults for zero values
	if opts.MaxOpenConns == 0 {
		opts.MaxOpenConns = DefaultOpenOptions().MaxOpenConns
	}
	if opts.MaxIdleConns == 0 {
		opts.MaxIdleConns = DefaultOpenOptions().MaxIdleConns
	}
	if opts.BusyTimeout == 0 {
		opts.BusyTimeout = DefaultOpenOptions().BusyTimeout
	}
	if opts.Binary == "" {
		opts.Binary = DefaultOpenOptions().Binary
	}

	ctx := context.Background()
	dbPath := filepath.Join(dataDir, "hive.db")

	// Open with pragmas for WAL mode, busy timeout, and foreign keys
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)&_pragma=foreign_keys(ON)", dbPath, opts.BusyTimeout)
	conn, err := openPool(ctx, dsn, opts)
	if err != nil {
		return nil, err
	}

	dbVersion, binaryVersion, err := runMigrations(ctx, conn)
	if err != nil {
		if closeErr := conn.Close(); closeErr != nil {
			return nil, fmt.Errorf("failed to initialize schema: %w (close also failed: %w)", err, closeErr)
		}
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	db := &DB{
		conn:    conn,
		queries: New(conn),
		schema: schemaGuard{
			binary:        opts.Binary,
			dbVersion:     dbVersion,
			binaryVersion: binaryVersion,
		},
	}
	if !db.schema.ahead() {
		return db, nil
	}

	if err := conn.Close(); err != nil {
		return nil, fmt.Errorf("failed to close database: %w", err)
	}

	// The pragma is in the DSN so that each pooled connection gets it. A
	// PRAGMA statement sets only the connection that runs it.
	db.conn, err = openPool(ctx, dsn+"&_pragma=query_only(1)", opts)
	if err != nil {
		return nil, err
	}
	db.queries = New(&guardedConn{conn: db.conn, guard: db.schema})

	slog.Warn("hive.db schema is newer than this build, opened read-only",
		"db_version", dbVersion,
		"binary_version", binaryVersion,
		"binary", opts.Binary,
	)

	return db, nil
}

func openPool(ctx context.Context, dsn string, opts OpenOptions) (*sql.DB, error) {
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure connection pool - minimal connections for SQLite
	conn.SetMaxOpenConns(opts.MaxOpenConns)
	conn.SetMaxIdleConns(opts.MaxIdleConns)
	conn.SetConnMaxLifetime(0) // Connections live forever

	// Verify connectivity - fail fast for SQLite
	if err := conn.PingContext(ctx); err != nil {
		if closeErr := conn.Close(); closeErr != nil {
			return nil, fmt.Errorf("failed to connect to database: %w (close also failed: %w)", err, closeErr)
		}
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	return conn, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// Conn returns the underlying *sql.DB connection.
func (db *DB) Conn() *sql.DB {
	return db.conn
}

// Queries returns the sqlc queries interface.
func (db *DB) Queries() *Queries {
	return db.queries
}

// ReadOnly reports whether a newer build migrated the database. Writes then
// return *SchemaAheadError.
func (db *DB) ReadOnly() bool {
	return db.schema.ahead()
}

// SchemaVersions returns the schema version of the database and the newest
// schema version that this build knows.
func (db *DB) SchemaVersions() (dbVersion, binaryVersion int) {
	return db.schema.dbVersion, db.schema.binaryVersion
}

// WriteError turns a write that a read-only DB refused into
// *SchemaAheadError. Queries and WithTx do this themselves, except for a
// query that returns one row: its error comes from Scan, which the DB cannot
// see. Callers that write through such a query pass the error through
// WriteError.
func (db *DB) WriteError(err error) error {
	return db.schema.translate(err)
}

// WithTx executes a function within a transaction.
// If the function returns an error, the transaction is rolled back.
func (db *DB) WithTx(ctx context.Context, fn func(*Queries) error) error {
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", db.schema.translate(err))
	}

	queries := db.queries.WithTx(tx)
	if err := fn(queries); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("transaction failed: %w (rollback also failed: %w)", err, rbErr)
		}
		return db.schema.translate(err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", db.schema.translate(err))
	}

	return nil
}
