package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func Load(workspaceRoot string, opts LoadOptions) (App, error) {
	trimmed := strings.TrimSpace(workspaceRoot)
	if trimmed == "" {
		return App{}, errors.New("workspace root is required")
	}
	return load(trimmed, true, opts)
}

func LoadGlobal(opts LoadOptions) (App, error) {
	return load("", false, opts)
}

// ResolvePersistenceRoot resolves the config+data root using the production
// precedence: an explicit flag, KENT_PERSISTENCE_ROOT, then ~/.kent.
func ResolvePersistenceRoot(explicitRoot string) (string, error) {
	root, _ := resolveConfigRoot(LoadOptions{ConfigRoot: explicitRoot})
	if root == "" {
		root = DefaultPersistence
	}
	return NormalizePersistenceRoot(root)
}

func load(workspaceRoot string, includeWorkspaceLayer bool, opts LoadOptions) (App, error) {
	absWorkspace := ""
	trimmedWorkspaceRoot := strings.TrimSpace(workspaceRoot)
	if trimmedWorkspaceRoot != "" {
		resolved, err := filepath.Abs(trimmedWorkspaceRoot)
		if err != nil {
			return App{}, fmt.Errorf("resolve workspace root: %w", err)
		}
		absWorkspace = resolved
	} else if includeWorkspaceLayer {
		return App{}, errors.New("workspace root is required")
	}

	// The config+data root is controlled by the --persistence-root flag
	// (opts.ConfigRoot) or the KENT_PERSISTENCE_ROOT env var, in that order.
	// It locates config.toml and roots all persistence. It is intentionally
	// NOT a config.toml setting (a file cannot relocate its own directory).
	configRoot, configRootSource := resolveConfigRoot(opts)
	if configRoot != "" {
		expanded, expandErr := expandTildePath(configRoot)
		if expandErr != nil {
			return App{}, fmt.Errorf("resolve persistence root: %w", expandErr)
		}
		configRoot = expanded
	}

	homeSettingsPath, err := resolveSettingsFilePathInRoot(configRoot)
	if err != nil {
		return App{}, err
	}
	homeSettingsExists, err := settingsFileExists(homeSettingsPath)
	if err != nil {
		return App{}, err
	}

	homeFileConfig := settingsFile{}
	if homeSettingsExists {
		homeFileConfig, err = readSettingsFile(homeSettingsPath)
		if err != nil {
			return App{}, err
		}
		if err := rejectRemovedPersistenceRootKey(homeFileConfig, homeSettingsPath); err != nil {
			return App{}, err
		}
	}
	workspaceSettingsPath := ""
	workspaceSettingsExists := false
	workspaceFileConfig := settingsFile{}
	if includeWorkspaceLayer {
		workspaceSettingsPath, err = resolveWorkspaceSettingsFilePath(absWorkspace)
		if err != nil {
			return App{}, err
		}
		workspaceSettingsExists, err = settingsFileExists(workspaceSettingsPath)
		if err != nil {
			return App{}, err
		}
		if workspaceSettingsExists {
			workspaceFileConfig, err = readSettingsFile(workspaceSettingsPath)
			if err != nil {
				return App{}, err
			}
			if err := rejectRemovedPersistenceRootKey(workspaceFileConfig, workspaceSettingsPath); err != nil {
				return App{}, err
			}
		}
	}

	state := configRegistry.defaultState()
	state.PersistenceRoot = DefaultPersistence
	sources := configRegistry.defaultSourceMap()
	sources["persistence_root"] = "default"

	if err := configRegistry.applyFile(homeFileConfig, homeSettingsPath, &state, sources); err != nil {
		return App{}, err
	}
	if err := appendSystemPromptFileFromConfig(homeFileConfig, homeSettingsPath, SystemPromptFileScopeHomeConfig, &state); err != nil {
		return App{}, err
	}
	if includeWorkspaceLayer {
		if err := configRegistry.applyFile(workspaceFileConfig, workspaceSettingsPath, &state, sources); err != nil {
			return App{}, err
		}
		if err := appendSystemPromptFileFromConfig(workspaceFileConfig, workspaceSettingsPath, SystemPromptFileScopeWorkspaceConfig, &state); err != nil {
			return App{}, err
		}
	}
	if err := configRegistry.applyEnv(os.LookupEnv, &state, sources); err != nil {
		return App{}, err
	}
	if err := configRegistry.applyCLI(opts, &state, sources); err != nil {
		return App{}, err
	}
	applyConfigRootPersistence(configRoot, configRootSource, &state, sources)
	inheritReviewerDefaultsWithSources(&state.Settings, sources)

	if err := configRegistry.validate(settingsState{Settings: state.Settings}, sources); err != nil {
		return App{}, err
	}

	absPersistenceRoot, err := preparePersistenceRoot(state.PersistenceRoot)
	if err != nil {
		return App{}, err
	}
	if _, err := writeManagedRGConfigFileForSettingsPath(homeSettingsPath); err != nil {
		return App{}, fmt.Errorf("write managed rg config: %w", err)
	}
	absWorktreeBaseDir, err := prepareWorktreeBaseDir(absPersistenceRoot, state.Settings.Worktrees.BaseDir)
	if err != nil {
		return App{}, err
	}
	state.Settings.Worktrees.BaseDir = absWorktreeBaseDir

	settingsPath := homeSettingsPath
	if workspaceSettingsExists {
		settingsPath = workspaceSettingsPath
	}
	settingsExists := homeSettingsExists || workspaceSettingsExists
	return App{
		AppName:         DefaultAppName,
		WorkspaceRoot:   absWorkspace,
		PersistenceRoot: absPersistenceRoot,
		Settings:        state.Settings,
		Source: SourceReport{
			SettingsPath:                  settingsPath,
			SettingsFileExists:            settingsExists,
			CreatedDefaultConfig:          false,
			HomeSettingsPath:              homeSettingsPath,
			HomeSettingsFileExists:        homeSettingsExists,
			WorkspaceSettingsPath:         workspaceSettingsPath,
			WorkspaceSettingsFileExists:   workspaceSettingsExists,
			WorkspaceSettingsLayerEnabled: includeWorkspaceLayer,
			Sources:                       sources,
		},
	}, nil
}

// resolveConfigRoot picks the explicit config+data root from the
// --persistence-root flag (opts.ConfigRoot) or the KENT_PERSISTENCE_ROOT env
// var, returning the trimmed root and a source label for the source report.
func resolveConfigRoot(opts LoadOptions) (root string, source string) {
	if trimmed := strings.TrimSpace(opts.ConfigRoot); trimmed != "" {
		return trimmed, "flag"
	}
	if trimmed := strings.TrimSpace(os.Getenv(PersistenceRootEnvName)); trimmed != "" {
		return trimmed, "env"
	}
	return "", "default"
}

func applyConfigRootPersistence(configRoot string, source string, state *settingsState, sources map[string]string) {
	if strings.TrimSpace(configRoot) == "" {
		return
	}
	// configRoot is already tilde-expanded in load(); preparePersistenceRoot
	// resolves it to an absolute path alongside the default path.
	state.PersistenceRoot = configRoot
	sources["persistence_root"] = source
}

// rejectRemovedPersistenceRootKey fails loads when a config.toml still declares
// persistence_root, which is no longer a settings key. A config file cannot
// relocate the directory it is read from, so the root is set via the
// --persistence-root flag or the KENT_PERSISTENCE_ROOT env var instead.
func rejectRemovedPersistenceRootKey(raw settingsFile, settingsPath string) error {
	if _, ok, err := lookupFileString(raw, []string{"persistence_root"}); ok || err != nil {
		return fmt.Errorf("%w (in %s)", errPersistenceRootInConfigFile, settingsPath)
	}
	return nil
}

func appendSystemPromptFileFromConfig(raw settingsFile, settingsPath string, scope SystemPromptFileScope, state *settingsState) error {
	path, ok, err := lookupFileString(raw, []string{"system_prompt_file"})
	if err != nil || !ok {
		return err
	}
	resolved, err := resolveConfigRelativePath(path, settingsPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(resolved) == "" {
		return nil
	}
	state.Settings.SystemPromptFiles = append(state.Settings.SystemPromptFiles, SystemPromptFile{Path: resolved, Scope: scope})
	return nil
}

func resolveConfigRelativePath(path string, settingsPath string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", nil
	}
	expanded, err := expandTildePath(trimmed)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(expanded) {
		return filepath.Abs(expanded)
	}
	baseDir := strings.TrimSpace(filepath.Dir(settingsPath))
	if baseDir == "" || baseDir == "." {
		return filepath.Abs(expanded)
	}
	return filepath.Abs(filepath.Join(baseDir, expanded))
}
