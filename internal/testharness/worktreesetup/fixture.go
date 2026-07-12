package worktreesetup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const helperConfigEnvironmentKey = "KENT_TEST_WORKTREE_SETUP_CONFIG"

type Options struct {
	MarkerRelativePath          string
	InvocationCountRelativePath string
	SkillName                   string
	SkillDescription            string
}

type Payload struct {
	SourceWorkspaceRoot string  `json:"source_workspace_root"`
	BranchName          string  `json:"branch_name"`
	WorktreeRoot        string  `json:"worktree_root"`
	SessionID           *string `json:"session_id"`
	ProjectID           string  `json:"project_id"`
	WorkspaceID         string  `json:"workspace_id"`
	WorktreeID          string  `json:"worktree_id"`
	CreatedBranch       bool    `json:"created_branch"`
}

type Invocation struct {
	Arguments   []string `json:"arguments"`
	WorkingDir  string   `json:"working_dir"`
	Stdin       []byte   `json:"stdin"`
	Environment []string `json:"environment"`
}

type Fixture struct {
	invocationPath              string
	markerRelativePath          string
	invocationCountRelativePath string
	skillName                   string
}

type helperConfig struct {
	InvocationPath              string `json:"invocation_path"`
	MarkerRelativePath          string `json:"marker_relative_path"`
	InvocationCountRelativePath string `json:"invocation_count_relative_path"`
	SkillName                   string `json:"skill_name"`
	SkillDescription            string `json:"skill_description"`
}

func New(t *testing.T, options Options) Fixture {
	t.Helper()
	root := t.TempDir()
	config := helperConfig{
		InvocationPath:              filepath.Join(root, "invocation.json"),
		MarkerRelativePath:          options.MarkerRelativePath,
		InvocationCountRelativePath: options.InvocationCountRelativePath,
		SkillName:                   options.SkillName,
		SkillDescription:            options.SkillDescription,
	}
	if config.InvocationCountRelativePath == "" {
		config.InvocationCountRelativePath = filepath.Join(".kent", "setup-invocations")
	}
	configPath := filepath.Join(root, "helper-config.json")
	body, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal worktree setup helper config: %v", err)
	}
	if err := os.WriteFile(configPath, body, 0o600); err != nil {
		t.Fatalf("write worktree setup helper config: %v", err)
	}
	t.Setenv(helperConfigEnvironmentKey, configPath)
	return Fixture{
		invocationPath:              config.InvocationPath,
		markerRelativePath:          config.MarkerRelativePath,
		invocationCountRelativePath: config.InvocationCountRelativePath,
		skillName:                   config.SkillName,
	}
}

func (Fixture) Executable() string {
	return os.Args[0]
}

func (f Fixture) Invocation() (Invocation, error) {
	body, err := os.ReadFile(f.invocationPath)
	if err != nil {
		return Invocation{}, err
	}
	var invocation Invocation
	if err := json.Unmarshal(body, &invocation); err != nil {
		return Invocation{}, err
	}
	return invocation, nil
}

func (f Fixture) MarkerPath(worktreeRoot string) string {
	return filepath.Join(worktreeRoot, f.markerRelativePath)
}

func (f Fixture) InvocationCount(worktreeRoot string) (int, error) {
	body, err := os.ReadFile(filepath.Join(worktreeRoot, f.invocationCountRelativePath))
	if err != nil {
		return 0, err
	}
	count, err := strconv.Atoi(string(body))
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (f Fixture) SkillPath(worktreeRoot string) string {
	return filepath.Join(worktreeRoot, ".kent", "skills", f.skillName, "SKILL.md")
}

func (i Invocation) Payload() (Payload, error) {
	var payload Payload
	if err := json.Unmarshal(i.Stdin, &payload); err != nil {
		return Payload{}, err
	}
	return payload, nil
}

func (i Invocation) Verify(expected Payload) error {
	if !reflect.DeepEqual(i.Arguments, []string{expected.SourceWorkspaceRoot, expected.BranchName, expected.WorktreeRoot}) {
		return fmt.Errorf("argv = %q, want [%q %q %q]", i.Arguments, expected.SourceWorkspaceRoot, expected.BranchName, expected.WorktreeRoot)
	}
	if i.WorkingDir != expected.WorktreeRoot {
		return fmt.Errorf("cwd = %q, want %q", i.WorkingDir, expected.WorktreeRoot)
	}
	stdinPayload, err := i.Payload()
	if err != nil {
		return fmt.Errorf("decode stdin payload: %w", err)
	}
	if !reflect.DeepEqual(stdinPayload, expected) {
		return fmt.Errorf("stdin payload = %+v, want %+v", stdinPayload, expected)
	}
	for _, expectedEnvironment := range []struct {
		key   string
		value string
	}{
		{key: "KENT_WORKTREE_SOURCE_WORKSPACE_ROOT", value: expected.SourceWorkspaceRoot},
		{key: "KENT_WORKTREE_BRANCH_NAME", value: expected.BranchName},
		{key: "KENT_WORKTREE_ROOT", value: expected.WorktreeRoot},
		{key: "KENT_WORKTREE_PROJECT_ID", value: expected.ProjectID},
		{key: "KENT_WORKTREE_WORKSPACE_ID", value: expected.WorkspaceID},
		{key: "KENT_WORKTREE_WORKTREE_ID", value: expected.WorktreeID},
		{key: "KENT_WORKTREE_CREATED_BRANCH", value: strconv.FormatBool(expected.CreatedBranch)},
	} {
		value, found, err := i.environmentValue(expectedEnvironment.key)
		if err != nil {
			return err
		}
		if !found || value != expectedEnvironment.value {
			return fmt.Errorf("environment %q = %q, present=%t, want %q", expectedEnvironment.key, value, found, expectedEnvironment.value)
		}
	}
	sessionID, sessionFound, err := i.environmentValue("KENT_WORKTREE_SESSION_ID")
	if err != nil {
		return err
	}
	if expected.SessionID == nil {
		if sessionFound {
			return fmt.Errorf("environment %q = %q, want absent", "KENT_WORKTREE_SESSION_ID", sessionID)
		}
	} else if !sessionFound || sessionID != *expected.SessionID {
		return fmt.Errorf("environment %q = %q, present=%t, want %q", "KENT_WORKTREE_SESSION_ID", sessionID, sessionFound, *expected.SessionID)
	}
	payloadJSON, found, err := i.environmentValue("KENT_WORKTREE_PAYLOAD_JSON")
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("environment %q is absent", "KENT_WORKTREE_PAYLOAD_JSON")
	}
	var environmentPayload Payload
	if err := json.Unmarshal([]byte(payloadJSON), &environmentPayload); err != nil {
		return fmt.Errorf("decode KENT_WORKTREE_PAYLOAD_JSON: %w", err)
	}
	if !reflect.DeepEqual(environmentPayload, expected) {
		return fmt.Errorf("KENT_WORKTREE_PAYLOAD_JSON = %+v, want %+v", environmentPayload, expected)
	}
	return nil
}

func (i Invocation) environmentValue(key string) (string, bool, error) {
	var value string
	found := false
	for _, entry := range i.Environment {
		entryKey, entryValue, hasValue := strings.Cut(entry, "=")
		if !hasValue || entryKey != key {
			continue
		}
		if found {
			return "", false, fmt.Errorf("environment has duplicate key %q", key)
		}
		value = entryValue
		found = true
	}
	return value, found, nil
}
