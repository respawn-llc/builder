package config

// ApplyLoadOptionsToSnapshot applies request-scoped CLI-equivalent overrides
// to an already loaded application configuration. It performs no filesystem or
// environment reads, so callers can keep one immutable config snapshot across
// validation and materialization.
func ApplyLoadOptionsToSnapshot(app App, opts LoadOptions) (App, error) {
	sources := cloneSourceMapOrDefault(app.Source.Sources)
	state := settingsState{
		Settings:        app.Settings,
		PersistenceRoot: app.PersistenceRoot,
	}
	if err := configRegistry.applyCLI(opts, &state, sources); err != nil {
		return App{}, err
	}
	inheritReviewerDefaultsWithSources(&state.Settings, sources)
	if err := configRegistry.validate(state, sources); err != nil {
		return App{}, err
	}
	next := app
	next.Settings = state.Settings
	next.Source.Sources = sources
	return next, nil
}
