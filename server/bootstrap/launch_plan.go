package bootstrap

import "core/server/launch"

type LaunchRequest = launch.BootstrapRequest
type LaunchPlan = launch.BootstrapPlan

func ResolveLaunchPlan(persistenceRoot string, request LaunchRequest) (LaunchPlan, error) {
	return launch.ResolveBootstrapPlan(persistenceRoot, request)
}
