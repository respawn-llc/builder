package session

import "errors"

// Sentinel errors produced by the session store and its loaders. Callers and
// tests match these with errors.Is rather than comparing rendered message text,
// which is free to change without affecting behavior. Dynamic context (ids,
// paths, underlying causes) is attached via fmt.Errorf("... %w", Err...).
var (
	// ErrSessionFileSymlink is returned when the event log is a symlink, which is
	// rejected for security reasons. It is exported so external callers of the
	// public snapshot/open API can detect it.
	ErrSessionFileSymlink = errors.New("session file must not be a symlink")

	// errPersistedSessionResolverRequired is returned when a persisted session is
	// opened without its authoritative structured-metadata resolver.
	errPersistedSessionResolverRequired = errors.New("persisted session resolver is required")
	errPersistenceObserverRequired      = errors.New("persistence observer is required")
	errEventLogReconcilerRequired       = errors.New("event log reconciliation observer is required")
	errEphemeralStoreCannotBeDurable    = errors.New("ephemeral session store cannot be made durable")

	// Resolver-record validation guards. Each names a distinct way a resolver
	// can return an invalid persisted session record.
	errResolverRecordMissingSessionDir  = errors.New("resolver returned persisted session record with missing session dir")
	errResolverRecordRelativeSessionDir = errors.New("resolver returned persisted session record whose session dir is not an absolute clean path")
	errResolverRecordMissingMetadata    = errors.New("resolver returned persisted session record with missing metadata")
	errResolverRecordMissingSessionID   = errors.New("resolver returned persisted session metadata with missing session id")
	errResolverRecordSessionIDMismatch  = errors.New("resolver returned persisted session metadata with a different session id")
	errResolverRecordSessionDirMismatch = errors.New("resolver returned a different session dir than the scoped open path")
)
