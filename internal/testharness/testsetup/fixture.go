package testsetup

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const helperConfigEnvironmentKey = "KENT_TEST_WORKTREE_SETUP_CONFIG"

type Options struct {
	MarkerRelativePath          *string
	InvocationCountRelativePath *string
	Skill                       *Skill
}

type Skill struct {
	Name        string
	Description string
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
	markerRelativePath          *string
	invocationCountRelativePath string
	skill                       *Skill
}

type helperConfig struct {
	InvocationPath              string  `json:"invocation_path"`
	MarkerRelativePath          *string `json:"marker_relative_path"`
	InvocationCountRelativePath string  `json:"invocation_count_relative_path"`
	Skill                       *Skill  `json:"skill"`
}

func New(t *testing.T, options Options) Fixture {
	t.Helper()
	validated, err := validateOptions(options)
	if err != nil {
		t.Fatalf("validate worktree setup fixture options: %v", err)
	}
	root := t.TempDir()
	config := helperConfig{
		InvocationPath:              filepath.Join(root, "invocation.json"),
		MarkerRelativePath:          validated.markerRelativePath,
		InvocationCountRelativePath: validated.invocationCountRelativePath,
		Skill:                       validated.skill,
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
		skill:                       config.Skill,
	}
}

func (Fixture) Executable() string {
	return os.Args[0]
}

func (Fixture) InstallInSourceWorkspace(t *testing.T, sourceWorkspaceRoot string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve worktree setup helper executable: %v", err)
	}
	info, err := os.Stat(executable)
	if err != nil {
		t.Fatalf("stat worktree setup helper executable: %v", err)
	}
	relativePath := filepath.Join("scripts", "worktree-setup-helper"+filepath.Ext(executable))
	destination := filepath.Join(sourceWorkspaceRoot, relativePath)
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatalf("create worktree setup helper directory: %v", err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatalf("open worktree setup helper executable: %v", err)
	}
	defer source.Close()
	destinationFile, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		t.Fatalf("create worktree setup helper executable: %v", err)
	}
	_, copyErr := io.Copy(destinationFile, source)
	closeErr := destinationFile.Close()
	if copyErr != nil {
		t.Fatalf("copy worktree setup helper executable: %v", copyErr)
	}
	if closeErr != nil {
		t.Fatalf("close worktree setup helper executable: %v", closeErr)
	}
	return relativePath
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
	if f.markerRelativePath == nil {
		panic("worktree setup fixture marker effect is not configured")
	}
	return filepath.Join(worktreeRoot, *f.markerRelativePath)
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
	if f.skill == nil {
		panic("worktree setup fixture skill effect is not configured")
	}
	return filepath.Join(worktreeRoot, ".kent", "skills", f.skill.Name, "SKILL.md")
}

type validatedOptions struct {
	markerRelativePath          *string
	invocationCountRelativePath string
	skill                       *Skill
}

func validateOptions(options Options) (validatedOptions, error) {
	invocationCountRelativePath := filepath.Join(".kent", "setup-invocations")
	if options.InvocationCountRelativePath != nil {
		validated, err := validateRelativePath("invocation count", *options.InvocationCountRelativePath)
		if err != nil {
			return validatedOptions{}, err
		}
		invocationCountRelativePath = validated
	}
	var markerRelativePath *string
	if options.MarkerRelativePath != nil {
		validated, err := validateRelativePath("marker", *options.MarkerRelativePath)
		if err != nil {
			return validatedOptions{}, err
		}
		markerRelativePath = &validated
	}
	var skill *Skill
	if options.Skill != nil {
		validated, err := validateSkill(*options.Skill)
		if err != nil {
			return validatedOptions{}, err
		}
		skill = &validated
	}
	return validatedOptions{
		markerRelativePath:          markerRelativePath,
		invocationCountRelativePath: invocationCountRelativePath,
		skill:                       skill,
	}, nil
}

func validateRelativePath(label string, path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("%s relative path must be non-empty when present", label)
	}
	cleaned := filepath.Clean(trimmed)
	if filepath.IsAbs(cleaned) || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s relative path = %q, want a descendant path", label, path)
	}
	return cleaned, nil
}

func validateSkill(skill Skill) (Skill, error) {
	name := strings.TrimSpace(skill.Name)
	if name == "" {
		return Skill{}, fmt.Errorf("setup skill name must be non-empty when present")
	}
	description := strings.TrimSpace(skill.Description)
	if description == "" {
		return Skill{}, fmt.Errorf("setup skill description must be non-empty when present")
	}
	return Skill{Name: name, Description: description}, nil
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
