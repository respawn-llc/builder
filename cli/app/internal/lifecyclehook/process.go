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

func (d *Dispatcher) invoke(event lifecyclecontract.Event) {
	defer func() { <-d.active }()
	payload, err := lifecyclecontract.Encode(event)
	if err != nil {
		d.report(Issue{Category: event.Category, Err: fmt.Errorf("encode lifecycle hook payload: %w", err)})
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, timeout)
	defer cancel()
	stderr, writerErr := boundedio.NewWriter(stderrLimit)
	if writerErr != nil {
		d.report(Issue{Category: event.Category, Err: writerErr})
		return
	}
	command := exec.CommandContext(ctx, d.command[0], d.command[1:]...)
	command.Stdin = strings.NewReader(string(payload))
	command.Stdout = io.Discard
	command.Stderr = stderr
	err = command.Run()
	if err == nil || (d.ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled)) {
		return
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("lifecycle hook timed out after %s", timeout)
	}
	d.report(Issue{
		Category: event.Category,
		Err:      err,
		Stderr:   stderr.String(),
	})
}

func (d *Dispatcher) report(issue Issue) {
	select {
	case <-d.ctx.Done():
	case d.issues <- issue:
	}
}
