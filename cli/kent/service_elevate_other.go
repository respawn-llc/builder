//go:build !windows

package main

// elevateServiceAction is a no-op off Windows; launchd/systemd installs run as
// the user with no elevation step.
func elevateServiceAction(_ serviceAction) (int, bool) {
	return 0, false
}
