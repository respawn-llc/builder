package launch

import (
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
)

type ReadOnlySessionModel struct {
	Name     string
	Provider ReadOnlySessionModelProvider
	Locked   bool
}

type ReadOnlySessionModelProvider struct {
	id llm.Provider
}

func newReadOnlySessionModelProvider(id string) (ReadOnlySessionModelProvider, error) {
	normalized := strings.TrimSpace(id)
	if normalized == "" {
		return ReadOnlySessionModelProvider{}, errors.New("read-only session model provider is required")
	}
	return ReadOnlySessionModelProvider{id: llm.Provider(normalized)}, nil
}

func (p ReadOnlySessionModelProvider) ID() string {
	if p.id == "" {
		panic("read-only session model provider is absent")
	}
	return string(p.id)
}

type ReadOnlySessionModelUnavailableError struct {
	Reason serverapi.SessionExecutionModelUnavailableReason
}

func (e *ReadOnlySessionModelUnavailableError) Error() string {
	return fmt.Sprintf("read-only session model is unavailable: %s", e.Reason)
}

type ReadOnlySessionModelInvalidError struct {
	Err error
}

func (e *ReadOnlySessionModelInvalidError) Error() string {
	return fmt.Sprintf("read-only session model is invalid: %v", e.Err)
}

func (e *ReadOnlySessionModelInvalidError) Unwrap() error {
	return e.Err
}

// ResolveReadOnlySessionModel derives only the model identity needed by
// descriptive read models. It deliberately does not open, repair, or update a
// session store.
func ResolveReadOnlySessionModel(app config.App, meta session.Meta) (ReadOnlySessionModel, error) {
	if meta.Locked != nil {
		name := strings.TrimSpace(meta.Locked.Model)
		if name == "" {
			return ReadOnlySessionModel{}, invalidReadOnlySessionModel(errors.New("locked session contract has no model"))
		}
		provider, err := resolveReadOnlySessionModelProvider(
			name,
			meta.Locked.ProviderContract.ProviderID,
		)
		if err != nil {
			return ReadOnlySessionModel{}, err
		}
		return ReadOnlySessionModel{
			Name:     name,
			Provider: provider,
			Locked:   true,
		}, nil
	}
	active := app.Settings
	if meta.Continuation != nil && meta.Continuation.AgentRole != nil {
		role := strings.TrimSpace(*meta.Continuation.AgentRole)
		if role == "" {
			return ReadOnlySessionModel{}, invalidReadOnlySessionModel(errors.New("continuation agent role is invalid"))
		}
		lookup := config.LookupSubagentRole(active, role)
		switch lookup.Status {
		case config.SubagentRoleLookupPresent:
			active = lookup.Role.Settings
		case config.SubagentRoleLookupInvalid:
			return ReadOnlySessionModel{}, invalidReadOnlySessionModel(errors.New("continuation agent role is invalid"))
		case config.SubagentRoleLookupMissing:
			return ReadOnlySessionModel{}, invalidReadOnlySessionModel(errors.New("continuation agent role is not configured"))
		}
	}
	name := strings.TrimSpace(active.Model)
	if name == "" {
		return ReadOnlySessionModel{}, &ReadOnlySessionModelUnavailableError{
			Reason: serverapi.SessionExecutionModelUnavailableNotConfigured,
		}
	}
	provider, err := resolveReadOnlySessionModelProvider(name, active.ProviderOverride)
	if err != nil {
		return ReadOnlySessionModel{}, err
	}
	return ReadOnlySessionModel{Name: name, Provider: provider}, nil
}

func resolveReadOnlySessionModelProvider(model, configuredProvider string) (ReadOnlySessionModelProvider, error) {
	if provider := strings.TrimSpace(configuredProvider); provider != "" {
		resolved, err := newReadOnlySessionModelProvider(provider)
		if err != nil {
			return ReadOnlySessionModelProvider{}, invalidReadOnlySessionModel(err)
		}
		return resolved, nil
	}
	inferred, err := llm.InferProviderFromModel(model)
	if err != nil {
		return ReadOnlySessionModelProvider{}, invalidReadOnlySessionModel(err)
	}
	resolved, err := newReadOnlySessionModelProvider(string(inferred))
	if err != nil {
		return ReadOnlySessionModelProvider{}, invalidReadOnlySessionModel(err)
	}
	return resolved, nil
}

func invalidReadOnlySessionModel(err error) error {
	return &ReadOnlySessionModelInvalidError{Err: err}
}
