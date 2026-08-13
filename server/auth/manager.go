package auth

import (
	"context"
	"time"

	sharedauth "core/shared/auth"
)

type Manager struct {
	store     Store
	refresher *OAuthRefresher
	now       func() time.Time
}

func (m *Manager) HasEnvAPIKey() bool {
	if m == nil || m.store == nil {
		return false
	}
	store, ok := m.store.(interface{ HasEnvAPIKey() bool })
	return ok && store.HasEnvAPIKey()
}

type CurrentStateResolution struct {
	Loaded  *State
	Current *State
}

func NewManager(store Store, refresher *OAuthRefresher, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{
		store:     store,
		refresher: refresher,
		now:       now,
	}
}

func (m *Manager) Load(ctx context.Context) (State, error) {
	return m.loadState(ctx, false)
}

func (m *Manager) StoredState(ctx context.Context) (State, error) {
	return m.loadState(ctx, true)
}

func (m *Manager) loadState(ctx context.Context, persistedOnly bool) (State, error) {
	if m.store == nil {
		return EmptyState(), nil
	}
	var (
		state State
		err   error
	)
	if persistedOnly {
		if loader, ok := m.store.(PersistedStateLoader); ok {
			state, err = loader.LoadPersisted(ctx)
		} else {
			state, err = m.store.Load(ctx)
		}
	} else {
		state, err = m.store.Load(ctx)
	}
	if err != nil {
		return State{}, err
	}
	if state.Scope == "" {
		state.Scope = ScopeGlobal
	}
	if err := state.Validate(); err != nil {
		return State{}, err
	}
	return state, nil
}

func (m *Manager) CurrentState(ctx context.Context) (State, error) {
	resolution, err := m.ResolveCurrentState(ctx)
	if err != nil {
		return State{}, err
	}
	return *resolution.Current, nil
}

func (m *Manager) ResolveCurrentState(ctx context.Context) (CurrentStateResolution, error) {
	state, err := m.Load(ctx)
	if err != nil {
		return CurrentStateResolution{}, err
	}
	resolution := CurrentStateResolution{Loaded: &state}
	current, err := m.resolveState(ctx, state)
	if err != nil {
		return resolution, err
	}
	resolution.Current = &current
	return resolution, nil
}

func (m *Manager) EnsureStartupReady(ctx context.Context) error {
	state, err := m.Load(ctx)
	if err != nil {
		return err
	}
	return EnsureStartupReady(state)
}

func (m *Manager) SwitchMethod(ctx context.Context, method Method, isIdle bool) (State, error) {
	return m.SwitchMethodAndSetEnvAPIKeyPreference(ctx, method, EnvAPIKeyPreferenceUnspecified, false, isIdle)
}

func (m *Manager) SwitchMethodAndSetEnvAPIKeyPreference(
	ctx context.Context,
	method Method,
	preference EnvAPIKeyPreference,
	setPreference bool,
	isIdle bool,
) (State, error) {
	if err := sharedauth.EnsureIdleForMethodSwitch(isIdle); err != nil {
		return State{}, err
	}
	if err := method.Validate(); err != nil {
		return State{}, err
	}
	if setPreference {
		if err := preference.Validate(); err != nil {
			return State{}, err
		}
	}
	return m.updateState(ctx, func(state *State) error {
		state.Method = method
		if setPreference {
			state.EnvAPIKeyPreference = preference
		}
		return nil
	})
}

func (m *Manager) ClearMethod(ctx context.Context, isIdle bool) (State, error) {
	if err := sharedauth.EnsureIdleForMethodSwitch(isIdle); err != nil {
		return State{}, err
	}
	return m.updateState(ctx, func(state *State) error {
		state.Method = Method{Type: MethodNone}
		state.EnvAPIKeyPreference = EnvAPIKeyPreferenceUnspecified
		return nil
	})
}

func (m *Manager) SetEnvAPIKeyPreference(ctx context.Context, preference EnvAPIKeyPreference, isIdle bool) (State, error) {
	if err := sharedauth.EnsureIdleForMethodSwitch(isIdle); err != nil {
		return State{}, err
	}
	if err := preference.Validate(); err != nil {
		return State{}, err
	}
	return m.updateState(ctx, func(state *State) error {
		state.EnvAPIKeyPreference = preference
		return nil
	})
}

func (m *Manager) updateState(ctx context.Context, mutate func(*State) error) (State, error) {
	state, err := m.StoredState(ctx)
	if err != nil {
		return State{}, err
	}
	state.Scope = ScopeGlobal
	if mutate != nil {
		if err := mutate(&state); err != nil {
			return State{}, err
		}
	}
	state.UpdatedAt = m.now().UTC()
	if m.store != nil {
		if err := m.store.Save(ctx, state); err != nil {
			return State{}, err
		}
	}
	return state, nil
}

func (m *Manager) AuthorizationHeader(ctx context.Context) (string, error) {
	state, err := m.CurrentState(ctx)
	if err != nil {
		return "", err
	}
	if !state.IsConfigured() {
		return "", ErrAuthNotConfigured
	}
	return state.Method.AuthHeaderValue()
}

// OpenAIAuthMetadata exposes auth mode details for OpenAI transport behavior.
func (m *Manager) OpenAIAuthMetadata(ctx context.Context) (method string, accountID string, err error) {
	state, err := m.CurrentState(ctx)
	if err != nil {
		return "", "", err
	}
	switch state.Method.Type {
	case MethodOAuth:
		if state.Method.OAuth != nil {
			return string(MethodOAuth), state.Method.OAuth.AccountID, nil
		}
		return string(MethodOAuth), "", nil
	case MethodAPIKey:
		return string(MethodAPIKey), "", nil
	default:
		return "", "", nil
	}
}

func (m *Manager) resolveState(ctx context.Context, state State) (State, error) {
	if !state.IsConfigured() {
		return state, nil
	}
	method := state.Method
	if m.refresher == nil {
		return state, nil
	}
	updated, refreshed, err := m.refresher.MaybeRefresh(ctx, method)
	if err != nil {
		return State{}, err
	}
	if !refreshed {
		return state, nil
	}
	state.Method = updated
	state.UpdatedAt = m.now().UTC()
	if m.store != nil {
		if err := m.store.Save(ctx, state); err != nil {
			return State{}, err
		}
	}
	return state, nil
}
