package runtimewire

import (
	"core/server/tools"
	"core/shared/config"
)

func ManagedWorktreeBaseRootResolver(persistenceRoot string) tools.ManagedWorktreeBaseRootResolver {
	return func() (string, error) {
		return config.LoadGlobalWorktreeBaseDir(persistenceRoot)
	}
}
