package main

import (
	"flag"
	"fmt"
)

type fixtureFlags struct {
	WorkspaceRoot   string
	PersistenceRoot string
	ScriptPath      string
	ObservationPath string
}

func parseFixtureFlags(args []string) (fixtureFlags, error) {
	flags := flag.NewFlagSet("kent-pty-fixture", flag.ContinueOnError)
	var parsed fixtureFlags
	flags.StringVar(&parsed.WorkspaceRoot, "workspace", "", "fixture workspace root")
	flags.StringVar(&parsed.PersistenceRoot, "persistence-root", "", "fixture persistence root")
	flags.StringVar(&parsed.ScriptPath, "script", "", "script file")
	flags.StringVar(&parsed.ObservationPath, "observations", "", "observation artifact path")
	if err := flags.Parse(args); err != nil {
		return fixtureFlags{}, err
	}
	if parsed.WorkspaceRoot == "" {
		return fixtureFlags{}, fmt.Errorf("workspace is required")
	}
	if parsed.PersistenceRoot == "" {
		return fixtureFlags{}, fmt.Errorf("persistence-root is required")
	}
	if parsed.ScriptPath == "" {
		return fixtureFlags{}, fmt.Errorf("script is required")
	}
	if parsed.ObservationPath == "" {
		return fixtureFlags{}, fmt.Errorf("observations is required")
	}
	return parsed, nil
}
