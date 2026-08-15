package session

import (
	"context"
	"errors"
	"strings"
)

// PersistedSessionRecord is the authoritative persisted session lookup result.
// On success, SessionDir must be a non-empty absolute normalized path to the
// scoped session directory. Meta should be nil only when metadata truly does
// not exist for an otherwise valid record.
type PersistedSessionRecord struct {
	SessionDir   string
	Meta         *Meta
	ContextFacts SessionContextFacts
}

// PersistedSessionResolver resolves authoritative persisted session metadata.
// ResolvePersistedSession must return a fully normalized SessionDir and a
// populated Meta on success, or a zero-value PersistedSessionRecord with a
// non-nil error on failure.
type PersistedSessionResolver interface {
	ResolvePersistedSession(ctx context.Context, sessionID string) (PersistedSessionRecord, error)
}

func ResolvePersistedSessionRecord(
	ctx context.Context,
	resolver PersistedSessionResolver,
	sessionID string,
) (PersistedSessionRecord, error) {
	if resolver == nil {
		return PersistedSessionRecord{}, errPersistedSessionResolverRequired
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return PersistedSessionRecord{}, errors.New("session id is required")
	}
	record, err := resolver.ResolvePersistedSession(ctx, id)
	if err != nil {
		return PersistedSessionRecord{}, err
	}
	if err := validatePersistedSessionRecord(id, record); err != nil {
		return PersistedSessionRecord{}, err
	}
	meta := cloneMeta(*record.Meta)
	record.Meta = &meta
	record.ContextFacts = normalizeSessionContextFacts(record.ContextFacts)
	return record, nil
}
