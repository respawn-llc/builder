package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"core/cli/clienterrors"
	"core/shared/llmerrors"
	"core/shared/protocol"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type runJSONResult struct {
	Status      string        `json:"status"`
	Result      string        `json:"result,omitempty"`
	SessionID   string        `json:"session_id,omitempty"`
	SessionName *string       `json:"session_name,omitempty"`
	ContinueID  string        `json:"continue_id,omitempty"`
	ContinueCmd string        `json:"continue_command,omitempty"`
	Warnings    []string      `json:"warnings,omitempty"`
	DurationMS  int64         `json:"duration_ms"`
	Error       *runJSONError `json:"error,omitempty"`
}

type runJSONError struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	AttemptedDepth *int   `json:"attempted_depth,omitempty"`
	MaxDepth       *int   `json:"max_depth,omitempty"`
}

type runOutputMode string

const (
	runOutputModeFinalText runOutputMode = "final-text"
	runOutputModeJSON      runOutputMode = "json"
)

func parseRunOutputMode(raw string) (runOutputMode, error) {
	switch runOutputMode(strings.TrimSpace(raw)) {
	case runOutputModeFinalText:
		return runOutputModeFinalText, nil
	case runOutputModeJSON:
		return runOutputModeJSON, nil
	default:
		return "", fmt.Errorf("invalid --output-mode value %q", raw)
	}
}

func runErrorMessage(err error) string {
	var policy *protocol.SubagentLaunchPolicyError
	if errors.As(err, &policy) {
		return policy.Error()
	}
	var denied *serverapi.SubagentLaunchDeniedError
	if errors.As(err, &denied) {
		target := ""
		if denied.Target != nil {
			target = strings.TrimSpace(*denied.Target)
		}
		switch denied.Kind {
		case serverapi.SubagentLaunchDenialTargetMissing:
			if len(denied.AvailableRoles) > 0 {
				return fmt.Sprintf("subagent role %q is unavailable; available roles: %s", target, strings.Join(denied.AvailableRoles, ", "))
			}
			return fmt.Sprintf("subagent role %q is unavailable", target)
		case serverapi.SubagentLaunchDenialNotCallable:
			return "the requested subagent launch is not allowed for this Kent session"
		case serverapi.SubagentLaunchDenialCallerMissing:
			return "the caller session no longer exists"
		case serverapi.SubagentLaunchDenialParentMissing:
			return "the parent session no longer exists"
		default:
			return "the subagent launch request is invalid"
		}
	}
	if message, ok := clienterrors.WorkflowTaskResumeConflictMessage(err); ok {
		return message
	}
	if message := llmerrors.UserFacingError(err); message != "" {
		return message
	}
	return err.Error()
}

func runErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "interrupted"
	}
	var denied *serverapi.SubagentLaunchDeniedError
	if errors.As(err, &denied) {
		return "subagent_denied"
	}
	var policy *protocol.SubagentLaunchPolicyError
	if errors.As(err, &policy) && policy.Kind == protocol.SubagentLaunchPolicyMaxDepthExceeded {
		return "subagent_max_depth_exceeded"
	}
	return "runtime"
}

func newRunJSONError(err error) *runJSONError {
	if err == nil {
		return nil
	}
	result := &runJSONError{
		Code:    runErrorCode(err),
		Message: runErrorMessage(err),
	}
	var policy *protocol.SubagentLaunchPolicyError
	if !errors.As(err, &policy) || policy.Kind != protocol.SubagentLaunchPolicyMaxDepthExceeded {
		return result
	}
	result.AttemptedDepth = textutil.Pointer(policy.AttemptedDepth)
	result.MaxDepth = textutil.Pointer(policy.MaxDepth)
	return result
}

func emitRunJSON(v runJSONResult) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "failed to encode JSON output: %v\n", err)
	}
}

func emitRunUsageError(mode runOutputMode, message string) {
	if mode == runOutputModeJSON {
		emitRunJSON(runJSONResult{
			Status: "error",
			Error:  &runJSONError{Code: "usage", Message: message},
		})
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, message)
}

func emitRunFinalText(w io.Writer, warnings []string, result string, continueHint string) {
	if w == nil {
		return
	}
	emitWarnings(w, warnings)
	trimmedResult := strings.TrimRight(result, "\n")
	trimmedHint := strings.TrimSpace(continueHint)
	switch {
	case trimmedResult != "" && trimmedHint != "":
		_, _ = fmt.Fprintf(w, "%s\n\n%s\n", trimmedResult, trimmedHint)
	case trimmedResult != "":
		_, _ = fmt.Fprintln(w, trimmedResult)
	case trimmedHint != "":
		_, _ = fmt.Fprintln(w, trimmedHint)
	}
}

func emitWarnings(w io.Writer, warnings []string) {
	if w == nil || len(warnings) == 0 {
		return
	}
	for _, warning := range warnings {
		trimmed := strings.TrimSpace(warning)
		if trimmed == "" {
			continue
		}
		_, _ = fmt.Fprintln(w, trimmed)
	}
	_, _ = fmt.Fprintln(w)
}

func inferRunOutputMode(args []string) runOutputMode {
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "--output-mode" || arg == "-output-mode":
			if i+1 >= len(args) {
				return runOutputModeFinalText
			}
			if mode, err := parseRunOutputMode(args[i+1]); err == nil {
				return mode
			}
			return runOutputModeFinalText
		case strings.HasPrefix(arg, "--output-mode="):
			if mode, err := parseRunOutputMode(strings.TrimPrefix(arg, "--output-mode=")); err == nil {
				return mode
			}
			return runOutputModeFinalText
		case strings.HasPrefix(arg, "-output-mode="):
			if mode, err := parseRunOutputMode(strings.TrimPrefix(arg, "-output-mode=")); err == nil {
				return mode
			}
			return runOutputModeFinalText
		}
	}
	return runOutputModeFinalText
}
