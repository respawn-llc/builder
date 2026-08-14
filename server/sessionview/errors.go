package sessionview

import "errors"

// errPersistedSessionResolverRequired is returned when a session-view operation
// is invoked without a configured persisted Session resolver. Callers and tests
// match it with errors.Is rather than comparing rendered message text.
var errPersistedSessionResolverRequired = errors.New("persisted Session resolver is required")
