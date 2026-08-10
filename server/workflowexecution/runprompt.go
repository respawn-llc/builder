package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"core/server/launch"
	"core/server/llm"
	"core/server/runtime"
	"core/server/sessionruntime"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

// WorkflowRunPromptSubmission is the one selected-existing-Session input that
// a RunPrompt-owned Workflow Exact executes.
type WorkflowRunPromptSubmission struct {
	Prompt     string
	AgentSteer *runtime.AgentSteer
}

// WorkflowRunPromptProfile supplies RunPrompt-owned launch behavior to the
// shared Current-Node Agent execution builder.
type WorkflowRunPromptProfile struct {
	Plan           launch.SessionPlan
	RuntimeOptions sessionruntime.AgentRuntimePlanOptions
	Submission     <-chan WorkflowRunPromptSubmission
	Ask            sessionruntime.ExecutionAskHandler
	OnActive       func()
	RecordResult   func(name string, assistant llm.Message)
}

type WorkflowRunPromptPreparation interface {
	Start(context.Context, WorkflowRunPromptProfile) error
	Stop(context.Context) error
	Wait(context.Context) (sessionruntime.ExecutionResult, error)
	Close(context.Context) error
}

type WorkflowRunPromptBeginResult struct {
	Handled     bool
	Preparation WorkflowRunPromptPreparation
}

type workflowRunPromptPreparation struct {
	controller *CurrentNodeController
	run        *currentNodeRun
	sessionID  runtimeids.SessionID

	startOnce sync.Once
	startErr  error
	handle    sessionruntime.ExecutionHandle
	stopOnce  sync.Once
	stopErr   error
}

func (c *CurrentNodeController) Begin(
	ctx context.Context,
	sessionID runtimeids.SessionID,
) (WorkflowRunPromptBeginResult, error) {
	if c == nil {
		return WorkflowRunPromptBeginResult{}, errors.New("current node workflow controller is required")
	}
	taskID, err := c.store.TaskIDForSession(ctx, sessionID)
	if err != nil {
		return WorkflowRunPromptBeginResult{}, err
	}
	if taskID == nil {
		return WorkflowRunPromptBeginResult{}, nil
	}

	var selected *currentNodeRun
	var resolution workflowstore.TaskAttentionResolution
	handled := false
	err = c.taskMutations.Run(ctx, *taskID, func(ctx context.Context) error {
		binding, found, err := c.store.ResolveDirectSessionCurrentNodeBinding(ctx, sessionID)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		handled = true
		if binding.TaskID != *taskID {
			return fmt.Errorf(
				"retained Session %q Task ownership changed from %q to %q during RunPrompt begin",
				sessionID,
				*taskID,
				binding.TaskID,
			)
		}
		if c.authority.SessionHasActiveOrRetiringExecution(sessionID) {
			return ErrWorkflowRunPromptSessionRunning
		}
		key, err := binding.CurrentNode.Key()
		if err != nil {
			return err
		}
		c.mu.Lock()
		_, exists := c.currentRunLocked(key)
		c.mu.Unlock()
		if exists {
			return ErrWorkflowRunPromptSessionRunning
		}

		prepared, err := c.store.PrepareTaskResume(ctx, binding.TaskID)
		if err != nil {
			return err
		}
		result := prepared.Result()
		if len(result.CreatedExecutableCurrentNodes) != 1 ||
			!result.CreatedExecutableCurrentNodes[0].Reference.Equal(binding.CurrentNode) {
			return errors.Join(
				fmt.Errorf("selected retained RunPrompt Resume did not create exactly Current Node %v", binding.CurrentNode),
				prepared.Rollback(),
			)
		}

		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(binding.TaskID); err != nil {
			c.mu.Unlock()
			return errors.Join(err, prepared.Rollback())
		}
		if _, exists := c.currentRunLocked(key); exists {
			c.mu.Unlock()
			return errors.Join(ErrWorkflowRunPromptSessionRunning, prepared.Rollback())
		}
		runIDs, err := c.stageExplicitRunsLocked(
			result.CreatedExecutableCurrentNodes,
			nil,
			workflowruntime.TaskPromptDeliveryResume,
		)
		if err != nil {
			c.mu.Unlock()
			return errors.Join(err, prepared.Rollback())
		}
		run := c.runs[runIDs[0]]
		run.runPromptProfileReady = make(chan struct{})
		run.preparation = func(runCtx context.Context) error {
			select {
			case <-run.runPromptProfileReady:
				return nil
			case <-runCtx.Done():
				return context.Cause(runCtx)
			}
		}
		if err := c.validateStagedRunsLocked(runIDs); err != nil {
			c.discardStagedRunsLocked(runIDs)
			c.mu.Unlock()
			return errors.Join(err, prepared.Rollback())
		}
		if err := prepared.Commit(); err != nil {
			c.discardStagedRunsLocked(runIDs)
			c.mu.Unlock()
			return err
		}
		run.launchContext, run.launchCancel = context.WithCancel(c.workerContext)
		run.admissionDone = make(chan struct{})
		if err := c.activateRunLocked(run, currentNodeRunLaunching); err != nil {
			c.mu.Unlock()
			panic(fmt.Sprintf("activate committed selected retained RunPrompt Run: %v", err))
		}
		selected = run
		c.mu.Unlock()
		resolution = result.TaskAttentionResolution
		go c.runAdmission(run.id)
		return nil
	})
	c.finalizeTaskAttentionResolution(resolution)
	if err != nil {
		return WorkflowRunPromptBeginResult{Handled: handled}, err
	}
	if !handled {
		return WorkflowRunPromptBeginResult{}, nil
	}
	return WorkflowRunPromptBeginResult{
		Handled: true,
		Preparation: &workflowRunPromptPreparation{
			controller: c,
			run:        selected,
			sessionID:  sessionID,
		},
	}, nil
}

func (p *workflowRunPromptPreparation) Start(ctx context.Context, profile WorkflowRunPromptProfile) error {
	if p == nil || p.controller == nil || p.run == nil {
		return errors.New("workflow RunPrompt preparation is required")
	}
	p.startOnce.Do(func() {
		if profile.Plan.Descriptor.SessionID() != p.sessionID {
			p.startErr = fmt.Errorf(
				"workflow RunPrompt plan Session %s does not match retained Session %s",
				profile.Plan.Descriptor.SessionID(),
				p.sessionID,
			)
			return
		}
		if profile.Submission == nil {
			p.startErr = errors.New("workflow RunPrompt submission is required")
			return
		}
		c := p.controller
		c.mu.Lock()
		run := c.runs[p.run.id]
		if run == nil || run != p.run || !run.launching() || c.currentRuns[run.key] != run.id {
			c.mu.Unlock()
			p.startErr = sessionruntime.ErrExecutionNoLongerLive
			return
		}
		run.runPromptProfile = &profile
		close(run.runPromptProfileReady)
		c.mu.Unlock()
		p.startErr = p.waitForExact(ctx)
		if p.startErr == nil {
			p.handle, p.startErr = p.currentExactHandle()
		}
	})
	return p.startErr
}

func (p *workflowRunPromptPreparation) waitForExact(ctx context.Context) error {
	for {
		p.controller.mu.Lock()
		run := p.controller.runs[p.run.id]
		if run == nil || run != p.run || p.controller.currentRuns[p.run.key] != p.run.id {
			admissionErr := p.run.admissionErr
			p.controller.mu.Unlock()
			return errors.Join(sessionruntime.ErrExecutionNoLongerLive, admissionErr)
		}
		if run.exact() {
			p.controller.mu.Unlock()
			return nil
		}
		changed := run.phaseChanged
		p.controller.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

func (p *workflowRunPromptPreparation) currentExactHandle() (sessionruntime.ExecutionHandle, error) {
	p.controller.mu.Lock()
	run := p.controller.runs[p.run.id]
	if run == nil || !run.exact() {
		p.controller.mu.Unlock()
		return nil, sessionruntime.ErrExecutionNoLongerLive
	}
	scopeID := *run.exactScopeID
	p.controller.mu.Unlock()
	handle, live := p.controller.authority.ExecutionByScope(scopeID)
	if !live {
		return nil, sessionruntime.ErrExecutionNoLongerLive
	}
	return handle, nil
}

func (p *workflowRunPromptPreparation) Stop(ctx context.Context) error {
	if p == nil || p.controller == nil || p.run == nil {
		return nil
	}
	p.stopOnce.Do(func() {
		p.stopErr = p.controller.Interrupt(ctx, InterruptSelector{
			TaskID:      p.run.reference.TaskID,
			SessionID:   &p.sessionID,
			CurrentNode: &p.run.reference,
		})
	})
	return p.stopErr
}

func (p *workflowRunPromptPreparation) Wait(ctx context.Context) (sessionruntime.ExecutionResult, error) {
	if p == nil || p.handle == nil {
		return sessionruntime.ExecutionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	return p.handle.Wait(ctx)
}

func (p *workflowRunPromptPreparation) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if p.handle == nil {
		return p.stopErr
	}
	if p.stopErr != nil {
		return p.stopErr
	}
	return p.handle.Close(ctx)
}

var ErrWorkflowRunPromptSessionRunning = errors.New("selected workflow Session has an owning Run")

var _ WorkflowRunPromptPreparation = (*workflowRunPromptPreparation)(nil)
