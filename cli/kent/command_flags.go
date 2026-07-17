package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"core/shared/config"
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

type commandHandler func([]string, io.Writer, io.Writer) int

type commandGroup struct {
	path   string
	usage  commandUsage
	routes map[string]commandHandler
}

func dispatchCommandGroup(args []string, stdout io.Writer, stderr io.Writer, group commandGroup) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	fs := newCommandFlagSet(config.Command+" "+group.path, stderr, group.usage)
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fs.Usage()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	if handler, ok := group.routes[args[0]]; ok {
		return handler(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "unknown %s command: %s\n\n", group.path, args[0])
	fs.Usage()
	return 2
}

func writeCommandJSON(stdout io.Writer, stderr io.Writer, value any) int {
	if err := json.NewEncoder(stdout).Encode(value); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
