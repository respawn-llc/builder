package main

import (
	"fmt"
	"os"

	"core/internal/architectureguard"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	if err := architectureguard.CheckNoProductionRecover(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := architectureguard.CheckNoAPICapabilityNegotiation(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := architectureguard.CheckNoProductionPTYCheckpointProtocol(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
