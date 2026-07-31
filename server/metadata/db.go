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
	"sync"

	"core/server/metadata/sqlitegen"
	"core/server/workflow/label"

	"github.com/pressly/goose/v3"
	goosedatabase "github.com/pressly/goose/v3/database"
	sqlitedriver "modernc.org/sqlite"
)

//go:embed migrations/*.up.sql
var migrationsFS embed.FS

const metadataSQLiteConnectionPoolSize = 8

// Goose logger is process-wide; metadata owns this setting and currently keeps
// routine migration status output silent unless debug logging is explicitly enabled.
var metadataMigrationDebugLogs = false
var metadataMigrationLogWriter io.Writer = os.Stderr

const labelCollationName = "kent_label_casefold_v1"

const (
	workflowIdentityMigrationVersion         int64 = 62
	workflowSessionAgentRoleMigrationVersion int64 = 63
	workflowIdentityViewName                       = "project_default_workflow_identity"
)

var registerMetadataSQLiteCollationsOnce sync.Once
var registerMetadataSQLiteCollationsErr error

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
	if err := registerMetadataSQLiteCollations(); err != nil {
		return err
	}
	if err := registerMetadataSQLiteFunctions(); err != nil {
		return err
	}
	provider, err := newMetadataMigrationProvider(db)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := repairWorkflowIdentityMigrationCollision(ctx, db, provider); err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply metadata migrations: %w", err)
	}
	return nil
}

// repairWorkflowIdentityMigrationCollision recognizes databases that recorded
// the former version-62 Session role migration before version 62 became the
// Workflow identity migration.
func repairWorkflowIdentityMigrationCollision(ctx context.Context, db *sql.DB, provider *goose.Provider) error {
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("read metadata migration version: %w", err)
	}
	if version < workflowIdentityMigrationVersion || version > workflowSessionAgentRoleMigrationVersion {
		return nil
	}
	definitions, err := sqlitegen.New(db).ListMetadataSchemaDefinitions(ctx)
	if err != nil {
		return fmt.Errorf("inspect metadata schema before migrations: %w", err)
	}
	for _, definition := range definitions {
		if definition.ObjectKind == "view" && definition.ObjectName == workflowIdentityViewName {
			return nil
		}
	}

	versionStore, err := goosedatabase.NewStore(goosedatabase.DialectSQLite3, goose.DefaultTablename)
	if err != nil {
		return fmt.Errorf("create metadata migration version store: %w", err)
	}
	for collidedVersion := workflowSessionAgentRoleMigrationVersion; collidedVersion >= workflowIdentityMigrationVersion; collidedVersion-- {
		if err := versionStore.Delete(ctx, db, collidedVersion); err != nil {
			return fmt.Errorf("repair metadata migration version %d: %w", collidedVersion, err)
		}
	}
	return nil
}

func newMetadataMigrationProvider(db *sql.DB) (*goose.Provider, error) {
	migrations, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("open embedded metadata migrations: %w", err)
	}
	var logger goose.Logger = goose.NopLogger()
	if metadataMigrationDebugLogs && metadataMigrationLogWriter != nil {
		logger = &metadataMigrationLogger{out: metadataMigrationLogWriter, debug: metadataMigrationDebugLogs}
	}
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		db,
		migrations,
		goose.WithLogger(logger),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return nil, fmt.Errorf("create metadata migration provider: %w", err)
	}
	return provider, nil
}

func registerMetadataSQLiteCollations() error {
	registerMetadataSQLiteCollationsOnce.Do(func() {
		registerMetadataSQLiteCollationsErr = sqlitedriver.RegisterCollationUtf8(
			labelCollationName,
			func(left string, right string) int {
				return label.Compare(label.Name(left), label.Name(right))
			},
		)
	})
	if registerMetadataSQLiteCollationsErr != nil {
		return fmt.Errorf("register metadata SQLite label collation %q: %w", labelCollationName, registerMetadataSQLiteCollationsErr)
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
