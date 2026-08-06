package workflowexecution

import (
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"core/server/workflow"
	"core/shared/runtimeids"
)

const workflowFinalizerTimeoutEvent = "workflow_finalizer_timeout"

type workflowFinalizerPhase string

const workflowFinalizerPhaseResult workflowFinalizerPhase = "result"

type interruptedFinalizerDiagnostic struct {
	TaskID         workflow.TaskID
	CurrentNode    workflow.CurrentNodeReference
	ScopeID        runtimeids.ExecutionScopeID
	RunPhase       currentNodeRunPhase
	FinalizerPhase workflowFinalizerPhase
	Canceled       bool
}

func superviseInterruptedFinalizer(
	wait func(),
	diagnostic interruptedFinalizerDiagnostic,
	deadline time.Duration,
) {
	if wait == nil {
		panic("interrupted finalizer supervisor requires a wait operation")
	}
	if deadline <= 0 {
		panic("interrupted finalizer supervisor requires a positive deadline")
	}
	started := time.Now()
	go func() {
		stopped := make(chan struct{})
		go func() {
			wait()
			close(stopped)
		}()
		timer := time.NewTimer(deadline)
		defer timer.Stop()
		select {
		case <-stopped:
			return
		case <-timer.C:
		}
		elapsed := time.Since(started)
		stacks := allGoroutineStacks()
		slog.Error(
			"workflow result finalizer did not stop after cancellation",
			"event", workflowFinalizerTimeoutEvent,
			"task_id", diagnostic.TaskID,
			"current_node", diagnostic.CurrentNode,
			"scope_id", diagnostic.ScopeID,
			"run_phase", diagnostic.RunPhase,
			"finalizer_phase", diagnostic.FinalizerPhase,
			"elapsed", elapsed,
			"canceled", diagnostic.Canceled,
			"goroutine_stacks", stacks,
		)
		panic(fmt.Sprintf(
			"workflow result finalizer timeout: task=%s current_node=%v scope=%s elapsed=%s",
			diagnostic.TaskID,
			diagnostic.CurrentNode,
			diagnostic.ScopeID,
			elapsed,
		))
	}()
}

func allGoroutineStacks() string {
	size := 256 * 1024
	for {
		buffer := make([]byte, size)
		written := runtime.Stack(buffer, true)
		if written < len(buffer) || size >= 8*1024*1024 {
			return string(buffer[:written])
		}
		size *= 2
	}
}

func (p currentNodeRunPhase) String() string {
	switch p {
	case currentNodeRunQueued:
		return "queued"
	case currentNodeRunReserved:
		return "reserved"
	case currentNodeRunGated:
		return "gated"
	case currentNodeRunRunning:
		return "running"
	default:
		return fmt.Sprintf("invalid(%d)", p)
	}
}
