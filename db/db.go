package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers "sqlite" driver
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the application's data access interface. All SQL lives here; no
// handler or service writes its own queries (§10, §12).
type Store interface {
	Ping(ctx context.Context) error
	Migrate() error
	Close() error
}

type store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at dsn, applies WAL mode and
// foreign-key enforcement, and returns a Store. The parent directory of the
// database file is created automatically.
func Open(dsn string) (Store, error) {
	if err := ensureDir(dsn); err != nil {
		return nil, err
	}

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db open: %w", err)
	}

	// SQLite best-practice pragmas: WAL for concurrent reads, FK enforcement.
	if _, err := sqlDB.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("db pragmas: %w", err)
	}

	return &store{db: sqlDB}, nil
}

func (s *store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *store) Close() error {
	return s.db.Close()
}

// Migrate runs all pending goose migrations from the embedded migrations/
// directory. Migrations are append-only and never edited once applied (§10).
func (s *store) Migrate() error {
	goose.SetBaseFS(migrationsFS)

	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}

	if err := goose.Up(s.db, "migrations"); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}

	return nil
}

// DB exposes the underlying *sql.DB for packages that need direct access
// (e.g. query files in future phases). Kept unexported from the interface so
// callers go through Store methods; accessed by sub-packages via a cast.
func (s *store) DB() *sql.DB { return s.db }

// ensureDir creates the parent directory for a SQLite DSN if it does not
// exist. Handles both plain paths and file: URIs.
func ensureDir(dsn string) error {
	path := dsn
	if strings.HasPrefix(path, "file:") {
		path = strings.TrimPrefix(path, "file:")
		if idx := strings.Index(path, "?"); idx >= 0 {
			path = path[:idx]
		}
	}
	if path == ":memory:" || path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}
