//go:build !windows

package main

func elevateServiceAction(_ serviceAction) (int, bool) {
	return 0, false
}
