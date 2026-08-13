package chatcontext

import (
	"errors"
	"strings"

	"core/shared/config"
)

// FixedRootWorkspaceResolver reloads an exact workspace while retaining the
// config and persistence root selected at server startup.
type FixedRootWorkspaceResolver struct {
	configRoot string
}

func NewFixedRootWorkspaceResolver(configRoot string) FixedRootWorkspaceResolver {
	return FixedRootWorkspaceResolver{configRoot: strings.TrimSpace(configRoot)}
}

func (r FixedRootWorkspaceResolver) Resolve(workspaceRoot string) (config.App, error) {
	if r.configRoot == "" {
		return config.App{}, errors.New("fixed config root is required")
	}
	return config.Load(workspaceRoot, config.LoadOptions{ConfigRoot: r.configRoot})
}
