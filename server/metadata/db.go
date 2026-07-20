package metadata

import (
	"database/sql"
	"embed"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.up.sql
var migrationsFS embed.FS

const metadataSQLiteConnectionPoolSize = 8

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
	db, err := sql.Open("sqlite", metadataSQLiteDSN(trimmedDatabasePath))
	if err != nil {
		return nil, fmt.Errorf("open metadata db: %w", err)
	}
	if err := runMigrations(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	db.SetMaxOpenConns(metadataSQLiteConnectionPoolSize)
	db.SetMaxIdleConns(metadataSQLiteConnectionPoolSize)
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
	goose.SetBaseFS(migrationsFS)
	var logger goose.Logger = goose.NopLogger()
	if metadataMigrationDebugLogs && metadataMigrationLogWriter != nil {
		logger = &metadataMigrationLogger{out: metadataMigrationLogWriter, debug: metadataMigrationDebugLogs}
	}
	goose.SetLogger(logger)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set metadata migration dialect: %w", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		return fmt.Errorf("apply metadata migrations: %w", err)
	}
	return nil
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
