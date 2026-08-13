package config

import (
	"maps"

	"core/shared/textutil"
)

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

func cloneSettingsState(in settingsState) settingsState {
	out := in
	out.Settings = cloneSettings(in.Settings)
	out.Client.Hooks.lifecycleCommand = append([]string(nil), in.Client.Hooks.lifecycleCommand...)
	return out
}

func cloneSettings(in Settings) Settings {
	out := in
	out.Shell.PostprocessHook = textutil.Pointer(in.Shell.PostprocessHook)
	out.SystemPromptFiles = append([]SystemPromptFile(nil), in.SystemPromptFiles...)
	out.EnabledTools = maps.Clone(in.EnabledTools)
	out.SkillToggles = maps.Clone(in.SkillToggles)
	if in.Subagents != nil {
		out.Subagents = make(map[string]SubagentRole, len(in.Subagents))
		for name, role := range in.Subagents {
			copied := role
			copied.Settings = cloneSettings(role.Settings)
			copied.Sources = maps.Clone(role.Sources)
			out.Subagents[name] = copied
		}
	}
	return out
}
