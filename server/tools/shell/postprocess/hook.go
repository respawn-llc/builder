package postprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

const (
	hookTimeout        = 5 * time.Second
	maxHookOutputBytes = 32 * 1024
)

type hookRequest struct {
	ToolName        toolspec.ID `json:"tool_name"`
	Command         string      `json:"command"`
	ParsedArgs      []string    `json:"parsed_args,omitempty"`
	CommandName     string      `json:"command_name,omitempty"`
	Workdir         string      `json:"workdir,omitempty"`
	OriginalOutput  string      `json:"original_output"`
	CurrentOutput   string      `json:"current_output"`
	ExitCode        *int        `json:"exit_code,omitempty"`
	Backgrounded    bool        `json:"backgrounded,omitempty"`
	MaxDisplayChars int         `json:"max_display_chars,omitempty"`
}

type hookResponse struct {
	Processed      bool   `json:"processed"`
	ReplacedOutput string `json:"replaced_output,omitempty"`
}

type userHookProcessor struct {
	hookPath string
}

func (p userHookProcessor) ID() string {
	return "user/hook"
}

func (p userHookProcessor) Process(ctx context.Context, envelope Envelope) (Decision, error) {
	req := envelope.Request
	hookPath, ok := resolveHookPath(p.hookPath)
	if !ok {
		return Decision{}, ProcessorError{Severity: FailureRecoverable, Message: "command postprocess hook unavailable"}
	}
	payload, err := json.Marshal(hookRequest{
		ToolName:        req.ToolName,
		Command:         req.CommandText,
		ParsedArgs:      append([]string(nil), req.ParsedArgs...),
		CommandName:     req.CommandName,
		Workdir:         req.Workdir,
		OriginalOutput:  envelope.OriginalOutput,
		CurrentOutput:   envelope.CurrentOutput,
		ExitCode:        textutil.CloneInt(req.ExitCode),
		Backgrounded:    req.Backgrounded,
		MaxDisplayChars: req.MaxDisplayChars,
	})
	if err != nil {
		return Decision{}, ProcessorError{Severity: FailureRecoverable, Message: "command postprocess hook request encode failed", Err: err}
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, hookPath)
	cmd.Env = tools.EnrichShellEnvForSession(os.Environ(), req.OwnerSessionID)
	cmd.Stdin = bytes.NewReader(payload)
	stdout := newLimitedBuffer(maxHookOutputBytes)
	stderr := newLimitedBuffer(maxHookOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Decision{}, ctx.Err()
		}
		return Decision{}, ProcessorError{Severity: FailureRecoverable, Message: hookFailureWarning(err, stderr.String()), Err: err}
	}

	var response hookResponse
	if err := json.Unmarshal(stdout.buffer.Bytes(), &response); err != nil {
		return Decision{}, ProcessorError{Severity: FailureRecoverable, Message: "command postprocess hook returned invalid JSON", Err: err}
	}
	if !response.Processed {
		return Skip(envelope), nil
	}
	return Continue(envelope.WithCurrent(response.ReplacedOutput), p.ID()), nil
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	remaining int64
	truncated bool
}

func newLimitedBuffer(limit int64) *limitedBuffer {
	if limit <= 0 {
		limit = maxHookOutputBytes
	}
	return &limitedBuffer{remaining: limit}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	if b.remaining <= 0 {
		b.truncated = true
		return written, nil
	}
	chunk := p
	if int64(len(chunk)) > b.remaining {
		chunk = chunk[:int(b.remaining)]
		b.truncated = true
	}
	_, _ = b.buffer.Write(chunk)
	b.remaining -= int64(len(chunk))
	return written, nil
}

func (b *limitedBuffer) String() string {
	text := b.buffer.String()
	if b.truncated {
		return text + "\n[hook output truncated]"
	}
	return text
}

var _ io.Writer = (*limitedBuffer)(nil)

func hookFailureWarning(err error, stderr string) string {
	trimmed := strings.TrimSpace(stderr)
	if trimmed == "" {
		return fmt.Sprintf("command postprocess hook failed: %v", err)
	}
	return fmt.Sprintf("command postprocess hook failed: %v: %s", err, trimmed)
}
