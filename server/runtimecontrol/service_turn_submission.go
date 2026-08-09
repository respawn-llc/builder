package runtimecontrol

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

type userTurnProjection struct {
	ExecutionText string
	HistoryText   string
}

func (s *Service) resolveUserTurnInput(ctx context.Context, sessionID string, input serverapi.RuntimeUserTurnInput) (userTurnProjection, error) {
	if input.Kind == runtimeinput.KindPromptCommand && (s == nil || s.promptCommands == nil) {
		return userTurnProjection{}, errors.New("prompt command resolver is required")
	}
	execution, err := input.ExecutionText(func(command runtimeinput.PromptCommand) (string, error) {
		return s.promptCommands.ResolvePromptCommand(ctx, sessionID, command.Name, command.Arguments)
	})
	if err != nil {
		return userTurnProjection{}, err
	}
	history, err := input.CanonicalHistoryText()
	if err != nil {
		return userTurnProjection{}, err
	}
	return userTurnProjection{ExecutionText: execution, HistoryText: history}, nil
}

func (s *Service) SubmitUserTurn(ctx context.Context, req serverapi.RuntimeSubmitUserTurnRequest) (serverapi.RuntimeSubmitUserTurnResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, err
	}
	sessionID := strings.TrimSpace(req.SessionID)
	projection, err := s.resolveUserTurnInput(ctx, req.SessionID, req.Input)
	if err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, err
	}
	var resp serverapi.RuntimeSubmitUserTurnResponse
	inputAccepted := false
	recordAccepted := func(queued bool) {
		if inputAccepted {
			return
		}
		inputAccepted = true
	}
	err = s.runAgentExecution(ctx, req.SessionID, func(runCtx context.Context, engine *runtime.Engine) error {
		defer func() {
			if !inputAccepted {
				return
			}
			s.launchPromptHistoryAppend(
				engine,
				sessionID,
				projection.HistoryText,
			)
		}()
		shouldCompact, err := engine.ShouldCompactBeforeUserMessage(runCtx, projection.ExecutionText)
		if err != nil {
			return err
		}
		compacted := false
		compactionBusy := false
		if shouldCompact {
			compactErr := s.runPreSubmitCompaction(runCtx, sessionID, engine)
			if compactErr != nil {
				if !errors.Is(compactErr, runtime.ErrAgentBusy) {
					return compactErr
				}
				compactionBusy = true
			} else {
				compacted = true
			}
		}
		if compactionBusy {
			queued, queueErr := engine.QueueUserMessage(projection.ExecutionText)
			if queueErr != nil {
				return queueErr
			}
			recordAccepted(true)
			resp = serverapi.RuntimeSubmitUserTurnResponse{Compacted: compacted, Steered: true, QueueItemID: queued.ID}
			return nil
		}
		msg, queued, err := engine.SubmitUserMessageOrSteerWithHooks(runCtx, projection.ExecutionText, nil, recordAccepted)
		if err != nil {
			return err
		}
		if queued != nil {
			resp = serverapi.RuntimeSubmitUserTurnResponse{Compacted: compacted, Steered: true, QueueItemID: queued.ID}
			return nil
		}
		resp = serverapi.RuntimeSubmitUserTurnResponse{Compacted: compacted}
		if msg.Content != nil {
			resp.Message = *msg.Content
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, serverapi.ErrSessionRunStarting) {
			resp, steered, steerErr := s.trySubmitUserTurnAsActiveExecution(ctx, sessionID, projection, req)
			if steerErr != nil {
				return serverapi.RuntimeSubmitUserTurnResponse{}, steerErr
			}
			if steered {
				return resp, nil
			}
		}
	}
	if err != nil && !inputAccepted {
		return serverapi.RuntimeSubmitUserTurnResponse{}, err
	}
	return resp, err
}

func (s *Service) trySubmitUserTurnAsActiveExecution(ctx context.Context, acceptedSessionID string, projection userTurnProjection, req serverapi.RuntimeSubmitUserTurnRequest) (serverapi.RuntimeSubmitUserTurnResponse, bool, error) {
	var resp serverapi.RuntimeSubmitUserTurnResponse
	steered := false
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, false, err
	}
	if s == nil || s.authority == nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, false, errors.New("session runtime authority is required")
	}
	err = s.withLiveExecutionRuntime(ctx, sessionID, func(_ context.Context, engine *runtime.Engine) error {
		item, accepted, err := engine.QueueUserMessageForActiveRun(ctx, projection.ExecutionText, nil)
		if errors.Is(err, runtime.ErrNoActiveLiveRun) {
			if !activeExecutionAllowsRuntimeBoundInput(runtimeactivity.ActiveStepFromProvider(engine)) {
				return serverapi.ErrSessionRunStarting
			}
			item, err = engine.QueueUserMessage(projection.ExecutionText)
			if err != nil {
				return err
			}
			accepted = true
		} else if err != nil {
			return err
		}
		if !accepted {
			return serverapi.ErrSessionRunStarting
		}
		resp = serverapi.RuntimeSubmitUserTurnResponse{Steered: true, QueueItemID: item.ID}
		steered = true
		if err != nil {
			return err
		}
		s.launchPromptHistoryAppend(
			engine,
			acceptedSessionID,
			projection.HistoryText,
		)
		return nil
	})
	if err != nil {
		return serverapi.RuntimeSubmitUserTurnResponse{}, steered, err
	}
	return resp, steered, nil
}

func activeExecutionAllowsRuntimeBoundInput(snapshot *runtimeactivity.ActiveStepSnapshot) bool {
	if snapshot == nil {
		return false
	}
	switch snapshot.ActiveKind {
	case clientui.RuntimeActivityActiveKindCompaction, clientui.RuntimeActivityActiveKindPreSubmitCompaction:
		return true
	default:
		return false
	}
}

func (s *Service) runPreSubmitCompaction(ctx context.Context, sessionID string, engine *runtime.Engine) error {
	_, err := engine.CompactContextForPreSubmitWithActiveHook(ctx, nil)
	return err
}
