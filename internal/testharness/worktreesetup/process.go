package worktreesetup

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

func init() {
	configPath, configured := os.LookupEnv(helperConfigEnvironmentKey)
	if !configured {
		return
	}
	if configPath == "" {
		os.Exit(1)
	}
	os.Exit(runHelper(configPath, os.Args[1:], os.Stdin))
}

func runHelper(configPath string, arguments []string, stdin io.Reader) int {
	body, err := os.ReadFile(configPath)
	if err != nil {
		return helperError(err)
	}
	var config helperConfig
	if err := json.Unmarshal(body, &config); err != nil {
		return helperError(err)
	}
	if len(arguments) != 3 {
		return helperError(fmt.Errorf("setup helper arguments = %q, want three positional inputs", arguments))
	}
	stdinBody, err := io.ReadAll(stdin)
	if err != nil {
		return helperError(err)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return helperError(err)
	}
	invocationBody, err := json.Marshal(Invocation{
		Arguments:   append([]string(nil), arguments...),
		WorkingDir:  workingDir,
		Stdin:       stdinBody,
		Environment: append([]string(nil), os.Environ()...),
	})
	if err != nil {
		return helperError(err)
	}
	if err := os.MkdirAll(filepath.Dir(config.InvocationPath), 0o755); err != nil {
		return helperError(err)
	}
	if err := os.WriteFile(config.InvocationPath, invocationBody, 0o600); err != nil {
		return helperError(err)
	}
	if err := writeWorktreeEffects(arguments[2], config); err != nil {
		return helperError(err)
	}
	return 0
}

func writeWorktreeEffects(worktreeRoot string, config helperConfig) error {
	countPath := filepath.Join(worktreeRoot, config.InvocationCountRelativePath)
	if err := os.MkdirAll(filepath.Dir(countPath), 0o755); err != nil {
		return err
	}
	count := 0
	if body, err := os.ReadFile(countPath); err == nil {
		count, err = strconv.Atoi(string(body))
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	count++
	if err := os.WriteFile(countPath, []byte(strconv.Itoa(count)), 0o644); err != nil {
		return err
	}
	if config.MarkerRelativePath != nil {
		markerPath := filepath.Join(worktreeRoot, *config.MarkerRelativePath)
		if err := os.MkdirAll(filepath.Dir(markerPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(markerPath, []byte("marker"), 0o644); err != nil {
			return err
		}
	}
	if config.Skill != nil {
		skillPath := filepath.Join(worktreeRoot, ".kent", "skills", config.Skill.Name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
			return err
		}
		contents := "---\nname: " + config.Skill.Name + "\ndescription: " + config.Skill.Description + "\n---\n"
		if err := os.WriteFile(skillPath, []byte(contents), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func helperError(err error) int {
	_, _ = fmt.Fprintln(os.Stderr, err)
	return 1
}
