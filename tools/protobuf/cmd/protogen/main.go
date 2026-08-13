package main

import (
	"fmt"
	"os"
	"path/filepath"

	"core/tools/protobuf/internal/protogen"
)

func main() {
	if len(os.Args) != 3 {
		fail("usage: protogen <ensure|generate|verify> <go|ts|all>")
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
	default:
		fail("usage: protogen <ensure|generate|verify> <go|ts|all>")
	}
	if err != nil {
		fail(err.Error())
	}
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
