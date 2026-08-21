package session

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/shared/config"
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
	meta, err := normalizePersistedSessionMeta(*record.Meta)
	if err != nil {
		return PersistedSessionRecord{}, err
	}
	record.Meta = &meta
	record.ContextFacts = normalizeSessionContextFacts(record.ContextFacts)
	return record, nil
}

func ResolveScopedPersistedSessionRecord(
	ctx context.Context,
	resolver PersistedSessionResolver,
	containerDir string,
	sessionID string,
) (PersistedSessionRecord, error) {
	record, err := ResolvePersistedSessionRecord(ctx, resolver, sessionID)
	if err != nil {
		return PersistedSessionRecord{}, err
	}
	expectedDir, err := ResolveScopedSessionDir(containerDir, sessionID)
	if err != nil {
		return PersistedSessionRecord{}, err
	}
	expectedIdentity, err := config.CanonicalPathIdentity(expectedDir)
	if err != nil {
		return PersistedSessionRecord{}, err
	}
	recordIdentity, err := config.CanonicalPathIdentity(record.SessionDir)
	if err != nil {
		return PersistedSessionRecord{}, err
	}
	if recordIdentity != expectedIdentity {
		return PersistedSessionRecord{}, fmt.Errorf(
			"session %q is outside workspace container: %w",
			strings.TrimSpace(sessionID),
			ErrOutsideWorkspaceContainer,
		)
	}
	return record, nil
}

func normalizePersistedSessionMeta(meta Meta) (Meta, error) {
	meta = cloneMeta(meta)
	if err := normalizeMetaContinuation(&meta); err != nil {
		return Meta{}, fmt.Errorf("validate session continuation: %w", err)
	}
	if err := normalizeMetaChatSettings(&meta); err != nil {
		return Meta{}, fmt.Errorf("validate session Chat settings: %w", err)
	}
	if meta.ActiveWorkflowAssignment != nil {
		assignment, err := normalizeMessageRecord(*meta.ActiveWorkflowAssignment)
		if err != nil {
			return Meta{}, fmt.Errorf("validate active workflow assignment: %w", err)
		}
		meta.ActiveWorkflowAssignment = &assignment
	}
	if err := normalizeMetaWorktreeReminder(&meta); err != nil {
		return Meta{}, fmt.Errorf("validate session worktree context: %w", err)
	}
	if err := validateMetaCategory(&meta); err != nil {
		return Meta{}, err
	}
	return meta, nil
}
