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
	var version *string
	var packagePath string
	var goos *string
	var goarch *string
	flag.StringVar(&output, "output", "./bin/kent", "output binary path")
	flag.Func("version", "embedded Kent version", setOptionalString(&version))
	flag.StringVar(&packagePath, "package", "./cli/kent", "Go main package")
	flag.Func("goos", "target GOOS", setOptionalString(&goos))
	flag.Func("goarch", "target GOARCH", setOptionalString(&goarch))
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected build arguments:", strings.Join(flag.Args(), " "))
		os.Exit(2)
	}

	if strings.TrimSpace(output) == "" {
		fmt.Fprintln(os.Stderr, "--output must not be blank")
		os.Exit(2)
	}
	if strings.TrimSpace(packagePath) == "" {
		fmt.Fprintln(os.Stderr, "--package must not be blank")
		os.Exit(2)
	}

	var embeddedVersion string
	if version == nil {
		data, err := os.ReadFile("VERSION")
		if err != nil {
			fmt.Fprintln(os.Stderr, "read VERSION:", err)
			os.Exit(1)
		}
		embeddedVersion = strings.TrimPrefix(strings.TrimSpace(string(data)), "v")
	} else {
		embeddedVersion = strings.TrimPrefix(*version, "v")
	}
	if strings.TrimSpace(embeddedVersion) == "" {
		fmt.Fprintln(os.Stderr, "Kent version must not be blank")
		os.Exit(2)
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

	ldflags := "-s -w -X core/shared/config.Version=" + embeddedVersion
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
	command.Env, err = buildEnvironment(goos, goarch)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := command.Run(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			os.Exit(exitError.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "start go build:", err)
		os.Exit(1)
	}
}

func setOptionalString(target **string) func(string) error {
	return func(value string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("value must not be blank")
		}
		*target = &value
		return nil
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

func buildEnvironment(goos, goarch *string) ([]string, error) {
	environment := os.Environ()
	cgoEnabled, configured := os.LookupEnv("CGO_ENABLED")
	if configured && strings.TrimSpace(cgoEnabled) == "" {
		return nil, fmt.Errorf("CGO_ENABLED must not be blank when configured")
	}
	if !configured {
		environment = append(environment, "CGO_ENABLED=0")
	}
	if goos != nil {
		environment = append(environment, "GOOS="+*goos)
	}
	if goarch != nil {
		environment = append(environment, "GOARCH="+*goarch)
	}
	return environment, nil
}
