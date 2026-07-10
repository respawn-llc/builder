package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"core/internal/testharness/pty/blackbox"
)

func main() {
	clientPath := flag.String("client", "", "compiled client executable")
	serverPath := flag.String("server", "./bin/kent", "standalone server executable")
	scenarioPath := flag.String("scenario", "", "strict scenario JSON")
	profile := flag.String("profile", string(blackbox.GoProfile), "client profile")
	flag.Parse()
	if *clientPath == "" || *scenarioPath == "" {
		fmt.Fprintln(os.Stderr, "--client and --scenario are required")
		os.Exit(2)
	}
	absoluteClient, err := filepath.Abs(*clientPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	absoluteServer, err := filepath.Abs(*serverPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	scenario, err := blackbox.LoadScenario(*scenarioPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	result := (blackbox.Runner{}).Run(blackbox.RunRequest{
		Scenario: scenario, Profile: blackbox.ClientProfile(*profile), ClientBinary: absoluteClient, ServerBinary: absoluteServer,
	})
	if result.Err != nil {
		fmt.Fprintf(os.Stderr, "%v; run_root=%s; artifacts=%s\n", result.Err, result.RunRoot, result.ArtifactDir)
		os.Exit(1)
	}
}
