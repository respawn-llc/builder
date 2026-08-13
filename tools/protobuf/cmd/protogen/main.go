package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"core/tools/protobuf/internal/protogen"
)

func main() {
	if len(os.Args) < 3 {
		fail(usage())
	}
	repositoryRoot, err := findRepositoryRoot()
	if err != nil {
		fail(err.Error())
	}
	targets, err := protogen.ResolveTargets(os.Args[2])
	if err != nil {
		fail(err.Error())
	}
	manager := protogen.NewManager(repositoryRoot)
	switch os.Args[1] {
	case "ensure":
		err = manager.Ensure(targets)
	case "generate":
		err = manager.GenerateTargets(targets)
	case "verify":
		err = manager.Verify(targets)
	case "clean":
		err = manager.Clean(targets)
	case "run":
		if len(os.Args) < 5 || os.Args[3] != "--" {
			fail(usage())
		}
		if protogen.OutputsReady(os.Getenv("KENT_PROTOBUF_OUTPUTS_READY"), targets) {
			err = runCommand(os.Args[4], os.Args[5:])
			break
		}
		err = manager.WithOutputs(targets, func() error {
			return runCommand(os.Args[4], os.Args[5:])
		})
	default:
		fail(usage())
	}
	if err != nil {
		fail(err.Error())
	}
}

func runCommand(name string, arguments []string) error {
	command := exec.Command(name, arguments...)
	command.Dir = os.Getenv("KENT_PROTOBUF_RUN_DIR")
	command.Env = append(
		environmentWithout("KENT_PROTOBUF_OUTPUTS_READY", "GOOS", "GOARCH"),
		"KENT_PROTOBUF_OUTPUTS_READY="+os.Args[2],
	)
	if value := os.Getenv("KENT_PROTOBUF_RUN_GOOS"); value != "" {
		command.Env = append(command.Env, "GOOS="+value)
	}
	if value := os.Getenv("KENT_PROTOBUF_RUN_GOARCH"); value != "" {
		command.Env = append(command.Env, "GOARCH="+value)
	}
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func environmentWithout(names ...string) []string {
	excluded := make(map[string]struct{}, len(names))
	for _, name := range names {
		excluded[name] = struct{}{}
	}
	result := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		name := item
		for index, character := range item {
			if character == '=' {
				name = item[:index]
				break
			}
		}
		if _, exists := excluded[name]; !exists {
			result = append(result, item)
		}
	}
	return result
}

func usage() string {
	return "usage: protogen <ensure|generate|verify|clean> <go|ts|all>\n" +
		"       protogen run <go|ts|all> -- <command> [argument ...]"
}

func findRepositoryRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "buf.yaml")); err == nil {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", fmt.Errorf("repository root containing buf.yaml not found")
		}
		directory = parent
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
