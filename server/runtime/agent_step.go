package runtime

import (
	"context"
	"errors"
	"fmt"

	"core/server/runtimecommand"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

type agentStepAdmissionState struct {
	current      *activeAgentStep
	boundary     *activeAgentStep
	reducerGrant AgentStepReducerGrant
	scopeID      runtimeids.ExecutionScopeID
}

type activeAgentStep struct {
	scopeID runtimeids.ExecutionScopeID
	origin  serverapi.RuntimeStepOrigin
	phase   agentStepPhase
}

type agentStepPhase uint8

const (
	agentStepRegistered agentStepPhase = iota + 1
	agentStepProviderRunning
)

type continueAgentStepDecision struct {
	Origin serverapi.RuntimeStepOrigin
}

type agentProviderStepDecision interface {
	providerStepID() string
}

func (d continueAgentStepDecision) providerStepID() string {
	return d.Origin.StepID
}

type unscopedProviderStepDecision struct {
	stepID string
}

func (d unscopedProviderStepDecision) providerStepID() string {
	return d.stepID
}

type agentStepBoundaryDecision interface {
	agentStepBoundaryDecision()
}

type prepareNextAgentStepDecision struct{}

func (prepareNextAgentStepDecision) agentStepBoundaryDecision() {}

type finishAgentTurnDecision struct{}

func (finishAgentTurnDecision) agentStepBoundaryDecision() {}

type noAgentStepBoundaryDecision struct{}

func (noAgentStepBoundaryDecision) agentStepBoundaryDecision() {}

type agentStepBoundaryRequest struct {
	continueTurn bool
}

type agentStepBoundaryWaitResult struct {
	grant        AgentStepReducerGrant
	continueTurn bool
	step         activeAgentStep
}

func (e *Engine) beginAgentProviderStep(
	ctx context.Context,
	fallbackStepID string,
) (agentProviderStepDecision, error) {
	snapshot := e.stepLifecycle.Snapshot()
	if snapshot == nil {
		return unscopedProviderStepDecision{stepID: fallbackStepID}, nil
	}
	if !isAgentStepCapable(snapshot.ActiveKind) {
		return nil, ErrActiveStepInactive
	}
	decision, err := submitRuntimeEventWithContext(
		e.lifecycleCtx,
		ctx,
		e,
		snapshot.RunID,
		func(admission runtimeEventAdmission, runID string) (continueAgentStepDecision, error) {
			return e.registerAgentProviderStep(admission, runID, true)
		},
	)
	return decision, err
}

func (e *Engine) registerAgentProviderStep(
	admission runtimeEventAdmission,
	runID string,
	providerStarting bool,
) (continueAgentStepDecision, error) {
	current := e.stepLifecycle.Snapshot()
	if current == nil || current.RunID != runID || !isAgentStepCapable(current.ActiveKind) {
		return continueAgentStepDecision{}, ErrActiveStepInactive
	}
	if e.agentSteps.current != nil {
		if e.agentSteps.current.origin.RunID == runID {
			if providerStarting {
				e.agentSteps.current.phase = agentStepProviderRunning
			}
			return continueAgentStepDecision{Origin: e.agentSteps.current.origin}, nil
		}
		panic(fmt.Sprintf(
			"begin Agent Step for Run %q while origin %+v remains completion-eligible",
			runID,
			e.agentSteps.current.origin,
		))
	}
	origin := serverapi.RuntimeStepOrigin{
		RunID:  runID,
		StepID: uuid.NewString(),
	}
	grant := e.agentSteps.reducerGrant
	var scopeID runtimeids.ExecutionScopeID
	if grant != nil {
		var err error
		scopeID, err = grant.RegisterNext(admission.Context(), origin)
		if err != nil {
			return continueAgentStepDecision{}, err
		}
		e.agentSteps.reducerGrant = nil
	} else if sink, ok := e.cfg.StepLifecycle.(AgentStepOriginLifecycleSink); ok {
		var err error
		scopeID, err = sink.AgentStepBegan(admission.Context(), origin)
		if err != nil {
			return continueAgentStepDecision{}, err
		}
	} else {
		scopeID = e.agentSteps.scopeID
		if scopeID.IsZero() {
			scopeID = runtimeids.NewExecutionScopeID()
		}
	}
	e.agentSteps.scopeID = scopeID
	phase := agentStepRegistered
	if providerStarting {
		phase = agentStepProviderRunning
	}
	e.agentSteps.current = &activeAgentStep{scopeID: scopeID, origin: origin, phase: phase}
	return continueAgentStepDecision{Origin: origin}, nil
}

func (e *Engine) completeAgentProviderBoundary(
	ctx context.Context,
	continueTurn bool,
) (agentStepBoundaryDecision, error) {
	if e == nil {
		return nil, errors.New("runtime engine is required")
	}
	if e.runtimeEvents == nil {
		return e.applyAgentStepBoundary(
			runtimeEventAdmission{engine: e},
			agentStepBoundaryRequest{continueTurn: continueTurn},
			nil,
		)
	}
	deferred, err := runtimecommand.Submit(
		e.lifecycleCtx,
		e.runtimeEvents,
		agentStepBoundaryRequest{continueTurn: continueTurn},
		func(
			command runtimecommand.Admission,
			request agentStepBoundaryRequest,
			complete func(agentStepBoundaryDecision, error),
		) error {
			admission := runtimeEventAdmission{engine: e, command: command}
			if e.agentSteps.current == nil {
				complete(noAgentStepBoundaryDecision{}, nil)
				return nil
			}
			if e.agentSteps.current.phase != agentStepProviderRunning {
				complete(noAgentStepBoundaryDecision{}, nil)
				return nil
			}
			decision, handleErr := e.applyAgentStepBoundary(admission, request, complete)
			if handleErr != nil || decision != nil {
				complete(decision, handleErr)
			}
			return nil
		},
	)
	if err != nil {
		return nil, runtimeSteeringError(err)
	}
	decision, err := deferred.Await(ctx)
	return decision, runtimeSteeringError(err)
}

func (e *Engine) applyAgentStepBoundary(
	admission runtimeEventAdmission,
	request agentStepBoundaryRequest,
	complete func(agentStepBoundaryDecision, error),
) (agentStepBoundaryDecision, error) {
	current := e.agentSteps.current
	if current == nil {
		return nil, nil
	}
	e.agentSteps.current = nil
	e.agentSteps.boundary = current
	sink, ok := e.cfg.StepLifecycle.(AgentStepOriginLifecycleSink)
	if !ok {
		return e.resumeReducerBoundaryGrant(
			admission,
			localAgentStepReducerGrant{engine: e},
			request.continueTurn,
		)
	}
	transfer, err := sink.AgentStepBoundary(admission.Context(), current.origin)
	if err != nil {
		return nil, err
	}
	switch typed := transfer.(type) {
	case AgentStepReducerBoundary:
		return e.resumeReducerBoundaryGrant(admission, typed.Grant, request.continueTurn)
	case AgentStepWorktreeBoundary:
		if typed.Wait == nil {
			return nil, errors.New("Agent Step Worktree boundary has no waiter")
		}
		if complete == nil {
			grant, waitErr := typed.Wait.Await(admission.Context())
			if waitErr != nil {
				return nil, waitErr
			}
			return e.resumeReducerBoundaryGrant(admission, grant, request.continueTurn)
		}
		err := admission.startWork(func(workCtx context.Context) {
			grant, waitErr := typed.Wait.Await(workCtx)
			decision, submitErr := submitRuntimeEventWithContext(
				e.lifecycleCtx,
				e.lifecycleCtx,
				e,
				agentStepBoundaryWaitResult{
					grant:        grant,
					continueTurn: request.continueTurn,
					step:         *current,
				},
				func(
					resultAdmission runtimeEventAdmission,
					result agentStepBoundaryWaitResult,
				) (agentStepBoundaryDecision, error) {
					if waitErr != nil {
						return nil, waitErr
					}
					return e.acceptReducerBoundaryGrant(
						resultAdmission,
						result.grant,
						result.continueTurn,
						result.step,
					)
				},
			)
			complete(decision, submitErr)
		})
		if err != nil {
			return nil, err
		}
		return nil, nil
	default:
		return nil, errors.New("Agent Step Boundary transfer is invalid")
	}
}

func (e *Engine) resumeReducerBoundaryGrant(
	admission runtimeEventAdmission,
	grant AgentStepReducerGrant,
	continueTurn bool,
) (agentStepBoundaryDecision, error) {
	if e.agentSteps.boundary == nil {
		return nil, errors.New("Agent Step reducer boundary has no closed Step")
	}
	return e.acceptReducerBoundaryGrant(
		admission,
		grant,
		continueTurn,
		*e.agentSteps.boundary,
	)
}

func (e *Engine) acceptReducerBoundaryGrant(
	admission runtimeEventAdmission,
	grant AgentStepReducerGrant,
	continueTurn bool,
	step activeAgentStep,
) (agentStepBoundaryDecision, error) {
	if grant == nil {
		return nil, errors.New("Agent Step reducer boundary has no grant")
	}
	if e.agentSteps.boundary == nil || *e.agentSteps.boundary != step {
		return nil, errors.Join(ErrActiveStepInactive, grant.Release())
	}
	if lifecycle, ok := e.cfg.StepLifecycle.(AgentStepScopeLifecycle); ok &&
		!lifecycle.AgentStepScopeLive(admission.Context(), step.scopeID) {
		e.invalidateAgentStepScope(step.scopeID, errBoundaryScopeFinalized)
		return nil, errors.Join(ErrActiveStepInactive, grant.Release())
	}
	selection := stepBoundarySelection(step.scopeID, step.origin)
	if !continueTurn {
		selection = turnBoundarySelection(step.scopeID, step.origin)
	}
	applied := humanBoundaryApplyResult{}
	assignmentsApplied := 0
	for {
		next := e.boundaryAgenda.peekNext(selection)
		if next == nil {
			break
		}
		switch next.(type) {
		case *humanBoundaryAgendaItem:
			human, err := e.applyHumanBoundaryPrefix(admission, step.origin.StepID, selection)
			applied.applied += human.applied
			if human.receipt.Committed {
				applied.receipt = human.receipt
			}
			if err != nil {
				e.agentSteps.boundary = nil
				return nil, errors.Join(err, grant.Release())
			}
		case *workflowAssignmentAgendaItem:
			count, err := e.applyWorkflowAssignmentBoundary(
				admission,
				step.origin.StepID,
				selection,
			)
			assignmentsApplied += count
			if err != nil {
				e.agentSteps.boundary = nil
				return nil, errors.Join(err, grant.Release())
			}
			goto reduced
		default:
			e.agentSteps.boundary = nil
			return nil, errors.Join(
				fmt.Errorf("unsupported short Boundary Agenda item %T", next),
				grant.Release(),
			)
		}
	}
reduced:
	e.agentSteps.boundary = nil
	if assignmentsApplied > 0 {
		return finishAgentTurnDecision{}, grant.Release()
	}
	if continueTurn || applied.applied > 0 {
		if e.agentSteps.reducerGrant != nil {
			panic("Agent Step reducer grant duplicated")
		}
		e.agentSteps.reducerGrant = grant
		return prepareNextAgentStepDecision{}, nil
	}
	return finishAgentTurnDecision{}, grant.Release()
}

func (e *Engine) failAgentStepScope(cause error) error {
	_, err := submitRuntimeEvent(
		e,
		cause,
		func(_ runtimeEventAdmission, failure error) (struct{}, error) {
			var scopeID runtimeids.ExecutionScopeID
			switch {
			case e.agentSteps.current != nil:
				scopeID = e.agentSteps.current.scopeID
			case e.agentSteps.boundary != nil:
				scopeID = e.agentSteps.boundary.scopeID
			default:
				return struct{}{}, nil
			}
			e.invalidateAgentStepScope(scopeID, failure)
			return struct{}{}, nil
		},
	)
	return err
}

func (e *Engine) invalidateAgentStepScope(scopeID runtimeids.ExecutionScopeID, cause error) {
	if e.agentSteps.current != nil && e.agentSteps.current.scopeID == scopeID {
		e.agentSteps.current = nil
	}
	if e.agentSteps.boundary != nil && e.agentSteps.boundary.scopeID == scopeID {
		e.agentSteps.boundary = nil
	}
	if e.agentSteps.reducerGrant != nil && e.agentSteps.scopeID == scopeID {
		e.surfaceRunError(e.agentSteps.reducerGrant.Release())
		e.agentSteps.reducerGrant = nil
	}
	e.boundaryAgenda.finalizeScope(scopeID, cause)
}

type localAgentStepReducerGrant struct {
	engine *Engine
}

func (g localAgentStepReducerGrant) RegisterNext(
	_ context.Context,
	_ serverapi.RuntimeStepOrigin,
) (runtimeids.ExecutionScopeID, error) {
	if g.engine == nil || g.engine.agentSteps.scopeID.IsZero() {
		return runtimeids.ExecutionScopeID{}, errors.New("local Agent Step scope is unavailable")
	}
	return g.engine.agentSteps.scopeID, nil
}

func (localAgentStepReducerGrant) Release() error {
	return nil
}

func boundaryDecision(continueTurn bool) agentStepBoundaryDecision {
	if continueTurn {
		return prepareNextAgentStepDecision{}
	}
	return finishAgentTurnDecision{}
}
