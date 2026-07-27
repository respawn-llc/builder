package lifecyclehook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"core/shared/boundedio"
	"core/shared/lifecyclecontract"
)

const (
	timeout     = 30 * time.Second
	stderrLimit = 4 * 1024
)

type timeoutError struct {
	Limit time.Duration
}

func (e *timeoutError) Error() string {
	return fmt.Sprintf("lifecycle hook timed out after %s", e.Limit)
}

func (d *Dispatcher) invoke(event lifecyclecontract.Event) {
	defer func() { <-d.active }()
	if issue := invokeHook(d.ctx, d.command, event); issue != nil {
		d.report(*issue)
	}
}

func invokeHook(parent context.Context, commandArgs []string, event lifecyclecontract.Event) *Issue {
	payload, err := lifecyclecontract.Encode(event)
	if err != nil {
		issue := NewProcessIssue(event.Category, fmt.Errorf("encode lifecycle hook payload: %w", err), "")
		return &issue
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	stderr, writerErr := boundedio.NewWriter(stderrLimit)
	if writerErr != nil {
		issue := NewProcessIssue(event.Category, writerErr, "")
		return &issue
	}
	command := exec.CommandContext(ctx, commandArgs[0], commandArgs[1:]...)
	command.Stdin = strings.NewReader(string(payload))
	command.Stdout = io.Discard
	command.Stderr = stderr
	err = command.Run()
	if err == nil || (parent.Err() != nil && errors.Is(ctx.Err(), context.Canceled)) {
		return nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = &timeoutError{Limit: timeout}
	}
	issue := NewProcessIssue(event.Category, err, stderr.String())
	return &issue
}

func (d *Dispatcher) report(issue Issue) {
	d.Report(issue)
}
