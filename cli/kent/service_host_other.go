//go:build !windows

package main

import (
	brand "core/shared/config"
	"fmt"
	"io"
)

func serviceHostRun(_ serviceSpec, _ io.Writer, stderr io.Writer) int {
	fmt.Fprintln(stderr, brand.Command+" service run is only supported on Windows; on this platform the service manager runs `"+brand.Command+" serve` directly")
	return 2
}
