package chatcontext

import (
	"errors"
	"strings"

	"core/shared/config"
)

// FixedRootWorkspaceResolver reloads an exact workspace while retaining the
// config root and explicit settings overrides selected at server startup.
type FixedRootWorkspaceResolver struct {
	configRoot           string
	startupWorkspaceRoot string
	startupLoadOptions   config.LoadOptions
}

func NewFixedRootWorkspaceResolver(configRoot string, startupWorkspaceRoot string, startupLoadOptions config.LoadOptions) FixedRootWorkspaceResolver {
	configRoot = strings.TrimSpace(configRoot)
	startupLoadOptions.ConfigRoot = configRoot
	return FixedRootWorkspaceResolver{
		configRoot:           configRoot,
		startupWorkspaceRoot: strings.TrimSpace(startupWorkspaceRoot),
		startupLoadOptions:   startupLoadOptions,
	}
}

func (r FixedRootWorkspaceResolver) Resolve(workspaceRoot string) (config.App, error) {
	if r.configRoot == "" {
		return config.App{}, errors.New("fixed config root is required")
	}
	loadOptions := config.LoadOptions{ConfigRoot: r.configRoot}
	if r.startupWorkspaceRoot != "" {
		requestedRoot, err := config.CanonicalWorkspaceRoot(workspaceRoot)
		if err != nil {
			return config.App{}, err
		}
		startupRoot, err := config.CanonicalWorkspaceRoot(r.startupWorkspaceRoot)
		if err != nil {
			return config.App{}, err
		}
		if requestedRoot == startupRoot {
			loadOptions = r.startupLoadOptions
		}
	}
	return config.Load(workspaceRoot, loadOptions)
}
