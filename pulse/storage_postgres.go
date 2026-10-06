package pulse

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"slices"
	"strings"
)

// PostgreSQL-backed [Storage], the backend several instances of an
// application can share: every instance writes to the same tables, so any
// instance's dashboard shows them all. It shares its implementation with
// SQLiteStorage (see storage_sql.go). Pulse's tables live in a schema of
// their own, "pulse" by default, so they can sit in the application's
// database without mixing with its tables.

// PostgresStorage persists Pulse metrics to PostgreSQL.
type PostgresStorage struct{ *sqlStore }

// DefaultPostgresSchema is the schema Pulse's tables go in unless another
// is configured.
const DefaultPostgresSchema = "pulse"

var postgresIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

// NewPostgresStorage creates Pulse's tables in schema (DefaultPostgresSchema
// when empty) of db if they don't exist yet, and returns a backend on them.
// db must use a PostgreSQL driver, for example:
//
//	import _ "github.com/jackc/pgx/v5/stdlib"
//
//	db, err := sql.Open("pgx", dsn)
//
// The caller keeps ownership of db: Close does not close it.
func NewPostgresStorage(db *sql.DB, appName, schema string) (*PostgresStorage, error) {
	if schema == "" {
		schema = DefaultPostgresSchema
	}
	if !postgresIdent.MatchString(schema) {
		return nil, fmt.Errorf("pulse/postgres: invalid schema name %q", schema)
	}
	d := &sqlDialect{postgres: true, schema: schema}
	if err := initPostgresSchema(context.Background(), db, d); err != nil {
		return nil, err
	}
	return &PostgresStorage{newSQLStore(db, d, appName, "", sqliteQueueSize, false)}, nil
}

// OpenPostgresStorage opens dsn with the registered PostgreSQL driver
// ("pgx" or "postgres") and calls [NewPostgresStorage]. Close closes the
// database.
func OpenPostgresStorage(dsn, appName, schema string) (*PostgresStorage, error) {
	driver := postgresDriver()
	if driver == "" {
		return nil, errors.New(`pulse/postgres: no PostgreSQL database/sql driver is registered; ` +
			`import one, for example _ "github.com/jackc/pgx/v5/stdlib"`)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("pulse/postgres: open: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pulse/postgres: connect: %w", err)
	}
	s, err := NewPostgresStorage(db, appName, schema)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	s.ownsDB = true
	return s, nil
}

// postgresDriver returns the name of a registered PostgreSQL driver, or "".
func postgresDriver() string {
	drivers := sql.Drivers()
	for _, name := range []string{"pgx", "postgres"} {
		if slices.Contains(drivers, name) {
			return name
		}
	}
	return ""
}

// postgresTypes translates sqliteSchema's column types.
var postgresTypes = strings.NewReplacer("INTEGER", "BIGINT", "REAL", "DOUBLE PRECISION", "BLOB", "BYTEA")

// initPostgresSchema creates the schema and tables, and adds columns added
// since they were created. Instances starting together would race to create
// the same tables, so it runs under a transaction-scoped advisory lock.
func initPostgresSchema(ctx context.Context, db *sql.DB, d *sqlDialect) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pulse/postgres: %w", err)
	}
	defer tx.Rollback() // no-op once committed

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, advisoryKey("schema:"+d.schema)); err != nil {
		return fmt.Errorf("pulse/postgres: lock schema: %w", err)
	}
	stmts := []string{`CREATE SCHEMA IF NOT EXISTS ` + d.schema}
	for _, stmt := range strings.Split(postgresTypes.Replace(sqliteSchema), ";") {
		if s := strings.TrimSpace(stmt); s != "" {
			stmts = append(stmts, d.sql(s))
		}
	}
	for _, c := range sqliteAddedColumns {
		stmts = append(stmts, d.sql(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s`,
			c.table, c.column, postgresTypes.Replace(c.definition))))
	}
	for _, stmt := range sqlAddedIndexes {
		stmts = append(stmts, d.sql(stmt))
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("pulse/postgres: schema %q: %w", firstLine(s), err)
		}
	}
	return tx.Commit()
}

// advisoryKey derives a PostgreSQL advisory-lock key from name.
func advisoryKey(name string) int64 {
	h := fnv.New64a()
	h.Write([]byte("pulse:" + name))
	return int64(h.Sum64())
}

var (
	_ Storage          = (*PostgresStorage)(nil)
	_ rollupStore      = (*PostgresStorage)(nil)
	_ lifecycleStore   = (*PostgresStorage)(nil)
	_ logStore         = (*PostgresStorage)(nil)
	_ capacityReporter = (*PostgresStorage)(nil)
	_ errorScrubber    = (*PostgresStorage)(nil)
)
