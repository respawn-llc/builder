//go:build !windows

package main

import (
	brand "core/shared/config"
	"fmt"
	"io"
)

// serviceHostRun is the in-process service host entry. Only Windows installs an
// SCM-style service that re-invokes the binary to host itself; on every other
// platform the OS manager (launchd/systemd) runs `serve` directly, so this entry
// is never registered and is rejected if invoked by hand.
func serviceHostRun(_ serviceSpec, _ io.Writer, stderr io.Writer) int {
	fmt.Fprintln(stderr, brand.Command+" service run is only supported on Windows; on this platform the service manager runs `"+brand.Command+" serve` directly")
	return 2
}
