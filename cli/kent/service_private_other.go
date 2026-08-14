//go:build !darwin

package main

import "io"

func runDarwinPrivateServiceMode(_ []string, _ io.Writer, _ io.Writer) (int, bool) {
	return 0, false
}
