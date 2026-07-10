package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"core/server/tools"
	"core/server/tools/shell/postprocess"
	"core/shared/valuecopy"
)

type writeStdinInput struct {
	SessionID       int    `json:"session_id"`
	Chars           string `json:"chars,omitempty"`
	YieldTimeMS     *int   `json:"yield_time_ms,omitempty"`
	MaxOutputTokens *int   `json:"max_output_tokens,omitempty"`
}

type WriteStdinTool struct {
	outputLimit int
	background  *Manager
}

type writeStdinOutput struct {
	Output              string `json:"output"`
	BackgroundSessionID int    `json:"background_session_id,omitempty"`
	BackgroundRunning   bool   `json:"background_running,omitempty"`
	Backgrounded        bool   `json:"backgrounded,omitempty"`
	BackgroundExitCode  *int   `json:"background_exit_code,omitempty"`
}

func NewWriteStdinTool(outputLimit int, background *Manager) *WriteStdinTool {
	if outputLimit <= 0 {
		outputLimit = defaultLimit
	}
	return &WriteStdinTool{outputLimit: outputLimit, background: background}
}

func (t *WriteStdinTool) Call(ctx context.Context, c tools.Call) (tools.Result, error) {
	if t.background == nil {
		return tools.ErrorResultWith(c, "write_stdin is not configured", marshalNoHTMLEscape), nil
	}
	var in writeStdinInput
	if err := json.Unmarshal(c.Input, &in); err != nil {
		return tools.ErrorResultWith(c, fmt.Sprintf("invalid input: %v", err), marshalNoHTMLEscape), nil
	}
	if in.SessionID <= 0 {
		return tools.ErrorResultWith(c, "session_id is required", marshalNoHTMLEscape), nil
	}
	yieldTime := defaultWriteYieldTime
	if in.YieldTimeMS != nil {
		yieldTime = time.Duration(*in.YieldTimeMS) * time.Millisecond
	}
	maxChars := t.outputLimit
	if in.MaxOutputTokens != nil && *in.MaxOutputTokens > 0 {
		maxChars = *in.MaxOutputTokens * 4
	}
	result, err := t.background.WriteStdin(ctx, WriteRequest{
		SessionID:      strconv.Itoa(in.SessionID),
		Input:          in.Chars,
		YieldTime:      yieldTime,
		MaxOutputChars: maxChars,
	})
	if err != nil {
		return tools.ErrorResultWith(c, formatToolCallError("write_stdin", err), marshalNoHTMLEscape), nil
	}
	if strings.TrimSpace(result.ToolError) != "" {
		return tools.ErrorResultWith(c, postprocess.JoinWarnings(result.Warning, result.ToolError), marshalNoHTMLEscape), nil
	}
	body, marshalErr := marshalNoHTMLEscape(writeStdinOutput{
		Output:              formatExecResponse(result),
		BackgroundSessionID: in.SessionID,
		BackgroundRunning:   result.Running,
		Backgrounded:        result.Backgrounded,
		BackgroundExitCode:  valuecopy.Pointer(result.ExitCode),
	})
	if marshalErr != nil {
		return tools.Result{}, marshalErr
	}
	toolResult := tools.Result{CallID: c.ID, Name: c.Name, Output: body}
	if result.RawOutputRequested || result.Truncated {
		toolResult.PresentationDelta = shellResultPresentationDelta(result.RawOutputRequested, result.Truncated, false)
	}
	return toolResult, nil
}
