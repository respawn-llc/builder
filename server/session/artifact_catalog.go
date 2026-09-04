package session

import "path/filepath"

const (
	eventsFile                           = "events.jsonl"
	eventLogPersistenceLockFile          = "events.jsonl.lock"
	appendRecoveryFile                   = "append-recovery.json"
	sessionRunLogFile                    = "steps.log"
	eventLogMigrationWorkspaceDir        = "events.jsonl.migration"
	eventLogMigrationWorkspaceMarkerFile = "kent-session-events-migration-v1"
	eventLogMigrationStagedLogFile       = "staged-events.jsonl"
	eventLogMigrationReadyMarkerFile     = "staged-events.ready"
)

func RunLogPath(sessionDir string) string {
	return filepath.Join(sessionDir, sessionRunLogFile)
}
