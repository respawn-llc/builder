package auth

import (
	"context"
	"strings"
)

type envLookup func(string) (string, bool)

type EnvAPIKeyOverrideStore struct {
	base      Store
	lookupEnv envLookup
}

func NewEnvAPIKeyOverrideStore(base Store, lookupEnv envLookup) *EnvAPIKeyOverrideStore {
	if lookupEnv == nil {
		lookupEnv = func(string) (string, bool) { return "", false }
	}
	return &EnvAPIKeyOverrideStore{base: base, lookupEnv: lookupEnv}
}

func (s *EnvAPIKeyOverrideStore) HasEnvAPIKey() bool {
	if s == nil || s.lookupEnv == nil {
		return false
	}
	_, present := s.lookupEnv("OPENAI_API_KEY")
	return present
}

func (s *EnvAPIKeyOverrideStore) LoadPersisted(ctx context.Context) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	if s == nil || s.base == nil {
		return EmptyState(), nil
	}
	if loader, ok := s.base.(PersistedStateLoader); ok {
		return loader.LoadPersisted(ctx)
	}
	return s.base.Load(ctx)
}

func (s *EnvAPIKeyOverrideStore) Load(ctx context.Context) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}

	state, err := s.LoadPersisted(ctx)
	if err != nil {
		return State{}, err
	}

	trimmed, ok := "", false
	if s != nil && s.lookupEnv != nil {
		if key, present := s.lookupEnv("OPENAI_API_KEY"); present {
			trimmed = strings.TrimSpace(key)
			ok = trimmed != ""
		}
	}
	if !ok {
		return state, nil
	}
	if state.EnvAPIKeyPreference == EnvAPIKeyPreferencePreferSaved {
		return state, nil
	}
	state.Scope = ScopeGlobal
	state.Method = Method{
		Type:   MethodAPIKey,
		APIKey: &APIKeyMethod{Key: trimmed},
	}
	return state, nil
}

func (s *EnvAPIKeyOverrideStore) Save(ctx context.Context, state State) error {
	if s == nil || s.base == nil {
		return nil
	}
	return s.base.Save(ctx, state)
}
