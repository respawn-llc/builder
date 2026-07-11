package metadata

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.up.sql
var migrationsFS embed.FS

// Goose logger is process-wide; metadata owns this setting and currently keeps
// routine migration status output silent unless debug logging is explicitly enabled.
var metadataMigrationDebugLogs = false
var metadataMigrationLogWriter io.Writer = os.Stderr

func openDatabaseAtPath(persistenceRoot string, databasePath string) (*sql.DB, error) {
	trimmedRoot, err := filepath.Abs(filepath.Clean(persistenceRoot))
	if err != nil {
		return nil, fmt.Errorf("resolve persistence root: %w", err)
	}
	trimmedDatabasePath, err := filepath.Abs(filepath.Clean(databasePath))
	if err != nil {
		return nil, fmt.Errorf("resolve metadata db path: %w", err)
	}
	rel, err := filepath.Rel(trimmedRoot, trimmedDatabasePath)
	if err != nil {
		return nil, fmt.Errorf("validate metadata db path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("metadata db path %q escapes persistence root %q: %w", trimmedDatabasePath, trimmedRoot, ErrPathEscapesPersistenceRoot)
	}
	if err := os.MkdirAll(filepath.Dir(trimmedDatabasePath), 0o755); err != nil {
		return nil, fmt.Errorf("create metadata db dir: %w", err)
	}
	migrationDB, err := sql.Open("sqlite", metadataMigrationSQLiteDSN(trimmedDatabasePath))
	if err != nil {
		return nil, fmt.Errorf("open metadata migration db: %w", err)
	}
	migrationDB.SetMaxOpenConns(1)
	if err := runMigrations(migrationDB); err != nil {
		_ = migrationDB.Close()
		return nil, err
	}
	if err := migrationDB.Close(); err != nil {
		return nil, fmt.Errorf("close metadata migration db: %w", err)
	}

	db, err := sql.Open("sqlite", metadataSQLiteDSN(trimmedDatabasePath))
	if err != nil {
		return nil, fmt.Errorf("open metadata db: %w", err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func metadataSQLiteDSN(databasePath string) string {
	u := url.URL{Scheme: "file", Path: sqliteFileURLPath(databasePath)}
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	return u.String()
}

func metadataMigrationSQLiteDSN(databasePath string) string {
	u, err := url.Parse(metadataSQLiteDSN(databasePath))
	if err != nil {
		panic(fmt.Sprintf("metadata migration DSN construction failed for %q: %v", databasePath, err))
	}
	q := u.Query()
	q.Set("_txlock", "exclusive")
	u.RawQuery = q.Encode()
	return u.String()
}

func sqliteFileURLPath(databasePath string) string {
	slashPath := strings.ReplaceAll(filepath.ToSlash(databasePath), "\\", "/")
	if len(slashPath) >= 2 && slashPath[1] == ':' && isASCIILetter(rune(slashPath[0])) {
		return "/" + slashPath
	}
	return slashPath
}

func isASCIILetter(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

func runMigrations(db *sql.DB) error {
	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		return err
	}
	if _, err := provider.Up(context.Background()); err != nil {
		return fmt.Errorf("apply metadata migrations: %w", err)
	}
	return nil
}

func newMetadataMigrationProvider(db *sql.DB, goMigrations ...*goose.Migration) (*goose.Provider, error) {
	var logger goose.Logger = goose.NopLogger()
	if metadataMigrationDebugLogs && metadataMigrationLogWriter != nil {
		logger = &metadataMigrationLogger{out: metadataMigrationLogWriter, debug: metadataMigrationDebugLogs}
	}
	migrations, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("open embedded metadata migrations: %w", err)
	}
	options := []goose.ProviderOption{
		goose.WithLogger(logger),
		goose.WithDisableGlobalRegistry(true),
	}
	if len(goMigrations) > 0 {
		options = append(options, goose.WithGoMigrations(goMigrations...))
	}
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		db,
		migrations,
		options...,
	)
	if err != nil {
		return nil, fmt.Errorf("create metadata migration provider: %w", err)
	}
	return provider, nil
}

type metadataMigrationLogger struct {
	out   io.Writer
	debug bool
}

func (l *metadataMigrationLogger) Fatalf(format string, v ...any) {
	if l == nil || !l.debug || l.out == nil {
		return
	}
	_, _ = fmt.Fprintf(l.out, format+"\n", v...)
}

func (l *metadataMigrationLogger) Printf(format string, v ...any) {
	if l == nil || !l.debug || l.out == nil {
		return
	}
	_, _ = fmt.Fprintf(l.out, format+"\n", v...)
}
