package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type workspaceOverlaySnapshot struct {
	globalState      settingsState
	globalSources    map[string]string
	environment      map[string]string
	loadOptions      LoadOptions
	configRoot       string
	configRootSource string
	homeSettingsPath string
	homePathIdentity string
	homeSettingsInfo os.FileInfo
	homeExists       bool
}

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

func LoadInteractive(workspaceRoot string, opts LoadOptions) (App, ClientSettings, error) {
	trimmed := strings.TrimSpace(workspaceRoot)
	if trimmed == "" {
		return App{}, ClientSettings{}, errors.New("workspace root is required")
	}
	loaded, err := loadAll(trimmed, true, opts)
	if err != nil {
		return App{}, ClientSettings{}, err
	}
	return loaded.App, loaded.Client, nil
}

type loadedConfig struct {
	App    App
	Client ClientSettings
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
	loaded, err := loadAll(workspaceRoot, includeWorkspaceLayer, opts)
	if err != nil {
		return App{}, err
	}
	return loaded.App, nil
}

func loadAll(workspaceRoot string, includeWorkspaceLayer bool, opts LoadOptions) (loadedConfig, error) {
	absWorkspace := ""
	trimmedWorkspaceRoot := strings.TrimSpace(workspaceRoot)
	if trimmedWorkspaceRoot != "" {
		resolved, err := filepath.Abs(trimmedWorkspaceRoot)
		if err != nil {
			return loadedConfig{}, fmt.Errorf("resolve workspace root: %w", err)
		}
		absWorkspace = resolved
	} else if includeWorkspaceLayer {
		return loadedConfig{}, errors.New("workspace root is required")
	}

	// The config+data root is controlled by the --persistence-root flag
	// (opts.ConfigRoot) or the KENT_PERSISTENCE_ROOT env var, in that order.
	// It locates config.toml and roots all persistence. It is intentionally
	// NOT a config.toml setting (a file cannot relocate its own directory).
	configRoot, configRootSource := resolveConfigRoot(opts)
	if configRoot != "" {
		expanded, expandErr := expandTildePath(configRoot)
		if expandErr != nil {
			return loadedConfig{}, fmt.Errorf("resolve persistence root: %w", expandErr)
		}
		configRoot = expanded
	}

	homeSettingsPath, err := resolveSettingsFilePathInRoot(configRoot)
	if err != nil {
		return loadedConfig{}, err
	}
	homeSettingsExists, err := settingsFileExists(homeSettingsPath)
	if err != nil {
		return loadedConfig{}, err
	}

	homeFileConfig := settingsFile{}
	if homeSettingsExists {
		homeFileConfig, err = readSettingsFile(homeSettingsPath)
		if err != nil {
			return loadedConfig{}, err
		}
		if err := rejectRemovedPersistenceRootKey(homeFileConfig, homeSettingsPath); err != nil {
			return loadedConfig{}, err
		}
	}
	workspaceSettingsPath := ""
	workspaceSettingsExists := false
	workspaceSettingsLayerEnabled := includeWorkspaceLayer
	workspaceFileConfig := settingsFile{}
	if includeWorkspaceLayer {
		workspaceSettingsPath, err = resolveWorkspaceSettingsFilePath(absWorkspace)
		if err != nil {
			return loadedConfig{}, err
		}
		workspaceSettingsExists, err = settingsFileExists(workspaceSettingsPath)
		if err != nil {
			return loadedConfig{}, err
		}
		if homeSettingsExists && workspaceSettingsExists {
			homeInfo, err := os.Stat(homeSettingsPath)
			if err != nil {
				return loadedConfig{}, err
			}
			workspaceInfo, err := os.Stat(workspaceSettingsPath)
			if err != nil {
				return loadedConfig{}, err
			}
			workspaceSettingsLayerEnabled = !os.SameFile(homeInfo, workspaceInfo)
		}
		if workspaceSettingsLayerEnabled && workspaceSettingsExists {
			workspaceFileConfig, err = readSettingsFile(workspaceSettingsPath)
			if err != nil {
				return loadedConfig{}, err
			}
			if err := rejectRemovedPersistenceRootKey(workspaceFileConfig, workspaceSettingsPath); err != nil {
				return loadedConfig{}, err
			}
		}
	}

	state := configRegistry.defaultState()
	state.PersistenceRoot = DefaultPersistence
	sources := configRegistry.defaultSourceMap()
	sources["persistence_root"] = "default"

	if err := configRegistry.applyFile(homeFileConfig, homeSettingsPath, settingsFileLayerGlobal, &state, sources); err != nil {
		return loadedConfig{}, err
	}
	if err := appendSystemPromptFileFromConfig(homeFileConfig, homeSettingsPath, SystemPromptFileScopeHomeConfig, &state); err != nil {
		return loadedConfig{}, err
	}
	globalState := cloneSettingsState(state)
	globalSources := cloneSourceMapOrDefault(sources)
	var homeSettingsInfo os.FileInfo
	var homePathIdentity string
	if homeSettingsExists {
		homeSettingsInfo, err = os.Stat(homeSettingsPath)
		if err != nil {
			return loadedConfig{}, err
		}
		homePathIdentity, err = CanonicalPathIdentity(homeSettingsPath)
		if err != nil {
			return loadedConfig{}, err
		}
	}
	if workspaceSettingsLayerEnabled {
		if err := configRegistry.applyFile(workspaceFileConfig, workspaceSettingsPath, settingsFileLayerWorkspace, &state, sources); err != nil {
			return loadedConfig{}, err
		}
		if err := appendSystemPromptFileFromConfig(workspaceFileConfig, workspaceSettingsPath, SystemPromptFileScopeWorkspaceConfig, &state); err != nil {
			return loadedConfig{}, err
		}
	}
	if err := configRegistry.applyEnv(os.LookupEnv, &state, sources); err != nil {
		return loadedConfig{}, err
	}
	if err := configRegistry.applyCLI(opts, &state, sources); err != nil {
		return loadedConfig{}, err
	}
	applyConfigRootPersistence(configRoot, configRootSource, &state, sources)
	inheritReviewerDefaultsWithSources(&state.Settings, sources)

	if err := configRegistry.validate(settingsState{Settings: state.Settings}, sources); err != nil {
		return loadedConfig{}, err
	}

	absPersistenceRoot, err := preparePersistenceRoot(state.PersistenceRoot)
	if err != nil {
		return loadedConfig{}, err
	}
	if _, err := writeManagedRGConfigFileForSettingsPath(homeSettingsPath); err != nil {
		return loadedConfig{}, fmt.Errorf("write managed rg config: %w", err)
	}
	absWorktreeBaseDir, err := prepareWorktreeBaseDir(absPersistenceRoot, state.Settings.Worktrees.BaseDir)
	if err != nil {
		return loadedConfig{}, err
	}
	state.Settings.Worktrees.BaseDir = absWorktreeBaseDir

	settingsPath := homeSettingsPath
	if workspaceSettingsLayerEnabled && workspaceSettingsExists {
		settingsPath = workspaceSettingsPath
	}
	settingsExists := homeSettingsExists || workspaceSettingsExists
	app := App{
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
			WorkspaceSettingsLayerEnabled: workspaceSettingsLayerEnabled,
			Sources:                       sources,
		},
	}
	app.workspaceOverlay = &workspaceOverlaySnapshot{
		globalState:      globalState,
		globalSources:    globalSources,
		environment:      captureEnvironment(),
		loadOptions:      opts,
		configRoot:       configRoot,
		configRootSource: configRootSource,
		homeSettingsPath: homeSettingsPath,
		homePathIdentity: homePathIdentity,
		homeSettingsInfo: homeSettingsInfo,
		homeExists:       homeSettingsExists,
	}
	return loadedConfig{
		App:    app,
		Client: state.Client,
	}, nil
}

// LoadWorkspaceOverlay reads one Workspace configuration file over a captured
// global startup snapshot. It never rereads the global file or process
// environment.
func LoadWorkspaceOverlay(global App, workspaceRoot string) (App, error) {
	loaded, err := loadWorkspaceOverlay(global, workspaceRoot)
	return loaded.App, err
}

func loadWorkspaceOverlay(global App, workspaceRoot string) (loadedConfig, error) {
	if global.workspaceOverlay == nil {
		return loadedConfig{}, errors.New("global configuration snapshot is required")
	}
	trimmed := strings.TrimSpace(workspaceRoot)
	if trimmed == "" {
		return loadedConfig{}, errors.New("workspace root is required")
	}
	absWorkspace, err := filepath.Abs(trimmed)
	if err != nil {
		return loadedConfig{}, fmt.Errorf("resolve workspace root: %w", err)
	}
	workspacePath, err := resolveWorkspaceSettingsFilePath(absWorkspace)
	if err != nil {
		return loadedConfig{}, err
	}
	workspaceExists, err := settingsFileExists(workspacePath)
	if err != nil {
		return loadedConfig{}, err
	}
	snapshot := global.workspaceOverlay
	workspaceEnabled := true
	if snapshot.homeExists && workspaceExists {
		workspaceIdentity, identityErr := CanonicalPathIdentity(workspacePath)
		if identityErr != nil {
			return loadedConfig{}, identityErr
		}
		workspaceEnabled = workspaceIdentity != snapshot.homePathIdentity
		if workspaceEnabled {
			workspaceInfo, statErr := os.Stat(workspacePath)
			if statErr != nil {
				return loadedConfig{}, statErr
			}
			workspaceEnabled = !os.SameFile(snapshot.homeSettingsInfo, workspaceInfo)
		}
	}
	workspaceFile := settingsFile{}
	if workspaceEnabled && workspaceExists {
		workspaceFile, err = readSettingsFile(workspacePath)
		if err != nil {
			return loadedConfig{}, err
		}
		if err := rejectRemovedPersistenceRootKey(workspaceFile, workspacePath); err != nil {
			return loadedConfig{}, err
		}
	}
	globalState := snapshot.globalState
	globalSources := snapshot.globalSources
	if global.WorkspaceRoot != "" && !global.Source.WorkspaceSettingsFileExists {
		globalState.Settings = cloneSettings(global.Settings)
		globalState.PersistenceRoot = global.PersistenceRoot
		globalSources = global.Source.Sources
	}
	state := cloneSettingsState(globalState)
	sources := cloneSourceMapOrDefault(globalSources)
	if workspaceEnabled {
		if err := configRegistry.applyFile(workspaceFile, workspacePath, settingsFileLayerWorkspace, &state, sources); err != nil {
			return loadedConfig{}, err
		}
		if err := appendSystemPromptFileFromConfig(workspaceFile, workspacePath, SystemPromptFileScopeWorkspaceConfig, &state); err != nil {
			return loadedConfig{}, err
		}
	}
	if err := configRegistry.applyEnv(snapshot.lookupEnv, &state, sources); err != nil {
		return loadedConfig{}, err
	}
	if err := configRegistry.applyCLI(snapshot.loadOptions, &state, sources); err != nil {
		return loadedConfig{}, err
	}
	applyConfigRootPersistence(snapshot.configRoot, snapshot.configRootSource, &state, sources)
	inheritReviewerDefaultsWithSources(&state.Settings, sources)
	if err := configRegistry.validate(settingsState{Settings: state.Settings}, sources); err != nil {
		return loadedConfig{}, err
	}
	absPersistenceRoot, err := preparePersistenceRoot(state.PersistenceRoot)
	if err != nil {
		return loadedConfig{}, err
	}
	if _, err := writeManagedRGConfigFileForSettingsPath(snapshot.homeSettingsPath); err != nil {
		return loadedConfig{}, fmt.Errorf("write managed rg config: %w", err)
	}
	absWorktreeBaseDir, err := prepareWorktreeBaseDir(absPersistenceRoot, state.Settings.Worktrees.BaseDir)
	if err != nil {
		return loadedConfig{}, err
	}
	state.Settings.Worktrees.BaseDir = absWorktreeBaseDir
	settingsPath := snapshot.homeSettingsPath
	if workspaceEnabled && workspaceExists {
		settingsPath = workspacePath
	}
	return loadedConfig{
		App: App{
			AppName:         DefaultAppName,
			WorkspaceRoot:   absWorkspace,
			PersistenceRoot: absPersistenceRoot,
			Settings:        state.Settings,
			Source: SourceReport{
				SettingsPath:                  settingsPath,
				SettingsFileExists:            snapshot.homeExists || workspaceExists,
				HomeSettingsPath:              snapshot.homeSettingsPath,
				HomeSettingsFileExists:        snapshot.homeExists,
				WorkspaceSettingsPath:         workspacePath,
				WorkspaceSettingsFileExists:   workspaceExists,
				WorkspaceSettingsLayerEnabled: workspaceEnabled,
				Sources:                       sources,
			},
			workspaceOverlay: snapshot,
		},
		Client: state.Client,
	}, nil
}

func captureEnvironment() map[string]string {
	environment := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if found {
			environment[key] = value
		}
	}
	return environment
}

func (s *workspaceOverlaySnapshot) lookupEnv(key string) (string, bool) {
	value, ok := s.environment[key]
	return value, ok
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
