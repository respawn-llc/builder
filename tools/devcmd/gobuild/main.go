package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	var output string
	var version string
	var packagePath string
	var goos string
	var goarch string
	flag.StringVar(&output, "output", "./bin/kent", "output binary path")
	flag.StringVar(&version, "version", "", "embedded Kent version")
	flag.StringVar(&packagePath, "package", "./cli/kent", "Go main package")
	flag.StringVar(&goos, "goos", "", "target GOOS")
	flag.StringVar(&goarch, "goarch", "", "target GOARCH")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected build arguments:", strings.Join(flag.Args(), " "))
		os.Exit(2)
	}

	if version == "" {
		data, err := os.ReadFile("VERSION")
		if err != nil {
			fmt.Fprintln(os.Stderr, "read VERSION:", err)
			os.Exit(1)
		}
		version = strings.TrimPrefix(strings.TrimSpace(string(data)), "v")
	} else {
		version = strings.TrimPrefix(version, "v")
	}

	resolvedOutput, err := resolveOutput(output)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(resolvedOutput), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "create build output directory:", err)
		os.Exit(1)
	}

	ldflags := "-s -w"
	if version != "" {
		ldflags += " -X core/shared/config.Version=" + version
	}
	command := exec.Command(
		"go",
		"build",
		"-trimpath",
		"-buildvcs=false",
		"-ldflags",
		ldflags,
		"-o",
		resolvedOutput,
		packagePath,
	)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	command.Env = buildEnvironment(goos, goarch)
	if err := command.Run(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			os.Exit(exitError.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "start go build:", err)
		os.Exit(1)
	}
}

func resolveOutput(path string) (string, error) {
	for links := 0; ; links++ {
		if links >= 40 {
			return "", fmt.Errorf("build output symlink chain exceeds 40 links: %s", path)
		}
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return path, nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect build output %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return path, nil
		}
		target, err := os.Readlink(path)
		if err != nil {
			return "", fmt.Errorf("read build output symlink %s: %w", path, err)
		}
		if filepath.IsAbs(target) {
			path = target
		} else {
			path = filepath.Join(filepath.Dir(path), target)
		}
	}
}

func buildEnvironment(goos, goarch string) []string {
	environment := os.Environ()
	if os.Getenv("CGO_ENABLED") == "" {
		environment = append(environment, "CGO_ENABLED=0")
	}
	if goos != "" {
		environment = append(environment, "GOOS="+goos)
	}
	if goarch != "" {
		environment = append(environment, "GOARCH="+goarch)
	}
	return environment
}
