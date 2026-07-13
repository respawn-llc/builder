package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"core/server/tools"
	"core/server/tools/shell/postprocess"
	"core/shared/transcript"
)

type execCommandInput struct {
	Cmd             string `json:"cmd"`
	Command         string `json:"command,omitempty"`
	Workdir         string `json:"workdir,omitempty"`
	Shell           string `json:"shell,omitempty"`
	Login           *bool  `json:"login,omitempty"`
	TTY             bool   `json:"tty,omitempty"`
	Raw             bool   `json:"raw,omitempty"`
	YieldTimeMS     *int   `json:"yield_time_ms,omitempty"`
	MaxOutputTokens *int   `json:"max_output_tokens,omitempty"`
}

type ExecCommandTool struct {
	workspaceRoot  string
	defaultShell   string
	defaultLogin   bool
	outputLimit    int
	background     *Manager
	ownerSessionID string
	postprocessor  *postprocess.Runner
}

func NewExecCommandTool(workspaceRoot string, outputLimit int, background *Manager, ownerSessionID string) *ExecCommandTool {
	defaultShell := strings.TrimSpace(os.Getenv("SHELL"))
	if defaultShell == "" {
		defaultShell = "/bin/sh"
	}
	if outputLimit <= 0 {
		outputLimit = defaultLimit
	}
	return &ExecCommandTool{
		workspaceRoot:  workspaceRoot,
		defaultShell:   defaultShell,
		defaultLogin:   true,
		outputLimit:    outputLimit,
		background:     background,
		ownerSessionID: strings.TrimSpace(ownerSessionID),
	}
}

func NewExecCommandToolWithPostprocessor(workspaceRoot string, outputLimit int, background *Manager, ownerSessionID string, runner *postprocess.Runner) *ExecCommandTool {
	tool := NewExecCommandTool(workspaceRoot, outputLimit, background, ownerSessionID)
	tool.postprocessor = runner
	return tool
}

func (t *ExecCommandTool) Call(ctx context.Context, c tools.Call) (tools.Result, error) {
	if t.background == nil {
		return tools.ErrorResultWith(c, "exec_command is not configured", marshalNoHTMLEscape), nil
	}
	var in execCommandInput
	if err := json.Unmarshal(c.Input, &in); err != nil {
		return tools.ErrorResultWith(c, fmt.Sprintf("invalid input: %v", err), marshalNoHTMLEscape), nil
	}
	cmdText := strings.TrimSpace(in.Cmd)
	if cmdText == "" {
		cmdText = strings.TrimSpace(in.Command)
	}
	if cmdText == "" {
		return tools.ErrorResultWith(c, "cmd is required", marshalNoHTMLEscape), nil
	}
	workdir := ResolveWorkdir(t.workspaceRoot, in.Workdir)
	resolvedShell := strings.TrimSpace(in.Shell)
	if resolvedShell == "" {
		resolvedShell = t.defaultShell
	}
	useLogin := t.defaultLogin
	if in.Login != nil {
		useLogin = *in.Login
	}
	argv := []string{resolvedShell}
	if useLogin {
		argv = append(argv, "-lc", cmdText)
	} else {
		argv = append(argv, "-c", cmdText)
	}
	var yieldTime time.Duration
	if in.YieldTimeMS != nil {
		yieldTime = time.Duration(*in.YieldTimeMS) * time.Millisecond
	}
	maxChars := t.outputLimit
	if in.MaxOutputTokens != nil && *in.MaxOutputTokens > 0 {
		maxChars = *in.MaxOutputTokens * 4
	}
	result, err := t.background.Start(ctx, ExecRequest{
		Command:        argv,
		DisplayCommand: cmdText,
		OwnerSessionID: t.ownerSessionID,
		OwnerRunID:     strings.TrimSpace(c.RunID),
		OwnerStepID:    strings.TrimSpace(c.StepID),
		Workdir:        workdir,
		YieldTime:      yieldTime,
		MaxOutputChars: maxChars,
		KeepStdinOpen:  in.TTY,
		Raw:            in.Raw,
		Postprocessor:  t.postprocessor,
	})
	if err != nil {
		return tools.ErrorResultWith(c, formatToolCallError("exec_command", err), marshalNoHTMLEscape), nil
	}
	if strings.TrimSpace(result.ToolError) != "" {
		return tools.ErrorResultWith(c, formatToolError(result.Warning, result.ToolError), marshalNoHTMLEscape), nil
	}
	body, marshalErr := marshalNoHTMLEscape(formatExecResponse(result))
	if marshalErr != nil {
		return tools.Result{}, marshalErr
	}
	toolResult := tools.Result{
		CallID: c.ID,
		Name:   c.Name,
		Output: body,
		PresentationDelta: shellResultPresentationDelta(
			in.Raw,
			result.Truncated,
			result.MovedToBackground,
			result.ExitCode,
		),
	}
	return toolResult, nil
}

func shellResultPresentationDelta(rawRequested bool, truncated bool, movedToBackground bool, shellExitCode *int) *transcript.ToolResultPresentationDelta {
	if !rawRequested && !truncated && !movedToBackground && shellExitCode == nil {
		return nil
	}
	delta := &transcript.ToolResultPresentationDelta{
		RawOutputRequested: rawRequested,
		OutputTruncated:    truncated,
		MovedToBackground:  movedToBackground,
	}
	if shellExitCode != nil {
		exitCode := *shellExitCode
		delta.ShellExitCode = &exitCode
	}
	return delta
}
