package config

import (
	"errors"
	"fmt"
	"strings"
)

const (
	worktreeBaseDirKey      = "worktrees.base_dir"
	worktreeSetupScriptKey  = "worktrees.setup_script"
	worktreeSetupTimeoutKey = "worktrees.setup_timeout_seconds"
)

// LoadWorktreeSetupSettings reads only the settings used by setup execution.
func LoadWorktreeSetupSettings(workspaceRoot string, persistenceRoot string) (WorktreeSettings, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		return WorktreeSettings{}, errors.New("workspace root is required")
	}
	workspacePath, err := resolveWorkspaceSettingsFilePath(workspaceRoot)
	if err != nil {
		return WorktreeSettings{}, err
	}
	return loadWorktreeSettings(
		persistenceRoot,
		[]string{workspacePath},
		worktreeSetupScriptKey,
		worktreeSetupTimeoutKey,
	)
}

func LoadGlobalWorktreeBaseDir(persistenceRoot string) (string, error) {
	settings, err := loadWorktreeSettings(persistenceRoot, nil, worktreeBaseDirKey)
	if err != nil {
		return "", err
	}
	return prepareWorktreeBaseDir(persistenceRoot, settings.BaseDir)
}

func loadWorktreeSettings(persistenceRoot string, additionalSettingsPaths []string, selectedKeys ...string) (WorktreeSettings, error) {
	homePath, err := resolveSettingsFilePathInRoot(persistenceRoot)
	if err != nil {
		return WorktreeSettings{}, err
	}
	settings, err := configRegistry.settingsForKeys(selectedKeys...)
	if err != nil {
		return WorktreeSettings{}, err
	}
	worktreeSettings, err := configRegistry.settingsForKeys(
		worktreeBaseDirKey,
		worktreeSetupScriptKey,
		worktreeSetupTimeoutKey,
	)
	if err != nil {
		return WorktreeSettings{}, err
	}
	keyTree := newFileKeyTree()
	for _, setting := range worktreeSettings {
		fileSetting, ok := setting.(fileKeyRegisteringSetting)
		if !ok {
			return WorktreeSettings{}, errors.New("worktree setting cannot validate file keys")
		}
		fileSetting.registerFileKeys(keyTree)
	}
	state := settingsState{PersistenceRoot: persistenceRoot}
	sources := map[string]string{}
	for _, setting := range settings {
		setting.applyDefault(&state)
	}
	settingsPaths := append([]string{homePath}, additionalSettingsPaths...)
	for _, path := range settingsPaths {
		file, err := readOptionalSettingsFile(path)
		if err != nil {
			return WorktreeSettings{}, err
		}
		if worktrees, exists := file["worktrees"]; exists {
			if err := validateSettingsFileKeys(settingsFile{"worktrees": worktrees}, keyTree); err != nil {
				return WorktreeSettings{}, err
			}
		}
		for _, setting := range settings {
			if err := setting.applyFile(file, path, &state, sources); err != nil {
				return WorktreeSettings{}, fmt.Errorf("apply settings file %s: %w", path, err)
			}
		}
	}
	return state.Settings.Worktrees, nil
}

func readOptionalSettingsFile(path string) (settingsFile, error) {
	exists, err := settingsFileExists(path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return settingsFile{}, nil
	}
	return readSettingsFile(path)
}

func (r settingsRegistry) settingsForKeys(keys ...string) ([]registrySetting, error) {
	selected := make([]registrySetting, 0, len(keys))
	for _, wanted := range keys {
		found := false
		for _, setting := range r.settings {
			keyed, ok := setting.(keyedRegistrySetting)
			if !ok || keyed.registryKey() != wanted {
				continue
			}
			selected = append(selected, setting)
			found = true
			break
		}
		if !found {
			return nil, fmt.Errorf("settings registry is missing %q", wanted)
		}
	}
	return selected, nil
}
