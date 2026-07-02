package workflowscript

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	CodeMissingPath         = "workflow.validation.script_path_missing"
	CodeRelativePathSkipped = "workflow.validation.script_path_relative_check_skipped"
	CodeWorktreeRootMissing = "workflow.validation.script_worktree_root_missing"
	CodePathNotFound        = "workflow.validation.script_path_not_found"
	CodePathInaccessible    = "workflow.validation.script_path_inaccessible"
	CodePathIsDirectory     = "workflow.validation.script_path_is_directory"
	CodePathNotExecutable   = "workflow.validation.script_path_not_executable"
)

const ReasonValidationFailed = "workflow_script_validation_failed"

type ValidationRequest struct {
	RawPath             string
	WorktreeRoot        string
	RequireWorktreeRoot bool
}

type Diagnostic struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	RawPath      string `json:"raw_path,omitempty"`
	ResolvedPath string `json:"resolved_path,omitempty"`
	Blocking     bool   `json:"blocking"`
	Skipped      bool   `json:"skipped"`
}

type ValidationError struct {
	Diagnostic Diagnostic
}

func (e ValidationError) Error() string {
	return e.Diagnostic.Message
}

func (e ValidationError) DetailJSON() string {
	body, err := json.Marshal(e.Diagnostic)
	if err != nil {
		return fmt.Sprintf(`{"code":%q,"message":%q,"blocking":true}`, e.Diagnostic.Code, e.Diagnostic.Message)
	}
	return string(body)
}

func Validate(req ValidationRequest) []Diagnostic {
	raw := strings.TrimSpace(req.RawPath)
	root := strings.TrimSpace(req.WorktreeRoot)
	if raw == "" {
		return []Diagnostic{{
			Code:     CodeMissingPath,
			Message:  "script_path is required",
			Blocking: true,
		}}
	}
	resolved := raw
	if !filepath.IsAbs(resolved) {
		if root == "" {
			diagnostic := Diagnostic{
				Code:     CodeRelativePathSkipped,
				Message:  fmt.Sprintf("relative script_path %q was not checked because no task worktree root is available", raw),
				RawPath:  raw,
				Blocking: false,
				Skipped:  true,
			}
			if req.RequireWorktreeRoot {
				diagnostic.Code = CodeWorktreeRootMissing
				diagnostic.Message = fmt.Sprintf("relative script_path %q requires a task worktree root", raw)
				diagnostic.Blocking = true
				diagnostic.Skipped = false
			}
			return []Diagnostic{diagnostic}
		}
		resolved = filepath.Join(root, resolved)
	}
	resolved = filepath.Clean(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		code := CodePathInaccessible
		message := fmt.Sprintf("stat script_path %q: %v", resolved, err)
		if errors.Is(err, os.ErrNotExist) {
			code = CodePathNotFound
			message = fmt.Sprintf("script_path %q does not exist", resolved)
		}
		return []Diagnostic{{
			Code:         code,
			Message:      message,
			RawPath:      raw,
			ResolvedPath: resolved,
			Blocking:     true,
		}}
	}
	if info.IsDir() {
		return []Diagnostic{{
			Code:         CodePathIsDirectory,
			Message:      fmt.Sprintf("script_path %q is a directory", resolved),
			RawPath:      raw,
			ResolvedPath: resolved,
			Blocking:     true,
		}}
	}
	if info.Mode().Perm()&0o111 == 0 {
		return []Diagnostic{{
			Code:         CodePathNotExecutable,
			Message:      fmt.Sprintf("script_path %q is not executable", resolved),
			RawPath:      raw,
			ResolvedPath: resolved,
			Blocking:     true,
		}}
	}
	return nil
}

func ResolveExecutable(req ValidationRequest) (string, error) {
	diagnostics := Validate(ValidationRequest{
		RawPath:             req.RawPath,
		WorktreeRoot:        req.WorktreeRoot,
		RequireWorktreeRoot: true,
	})
	for _, diagnostic := range diagnostics {
		if diagnostic.Blocking {
			return "", ValidationError{Diagnostic: diagnostic}
		}
	}
	raw := strings.TrimSpace(req.RawPath)
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw), nil
	}
	return filepath.Clean(filepath.Join(strings.TrimSpace(req.WorktreeRoot), raw)), nil
}
