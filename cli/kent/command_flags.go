package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
)

const persistenceRootFlagUsage = "Kent config and data directory; overrides KENT_PERSISTENCE_ROOT and ~/.kent"

func newCommandFlagSet(name string, stderr io.Writer, usage commandUsage) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { usage.write(fs) }
	return fs
}

func parseCommandFlags(fs *flag.FlagSet, args []string) (bool, int) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, 0
		}
		return false, 2
	}
	return true, 0
}

func writeCommandJSON(stdout io.Writer, stderr io.Writer, value any) int {
	if err := json.NewEncoder(stdout).Encode(value); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
