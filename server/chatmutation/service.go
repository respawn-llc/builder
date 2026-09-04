package chatmutation

import (
	"context"
	"errors"
	"fmt"

	"core/server/sessionruntime"
	"core/shared/protoapi"
	chatpb "core/shared/protoapi/gen/kent/api/chat"
	promptcommandpb "core/shared/protoapi/gen/kent/api/prompt_command"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"

	"google.golang.org/protobuf/types/known/emptypb"
)

type TargetResolutionService interface {
	Resolve(context.Context, TargetResolutionRequest) (ResolvedTarget, error)
}

type RuntimeOpeningService interface {
	Open(context.Context, runtimeids.SessionID) (RuntimeAttachment, error)
}

type RuntimeAdmissionService interface {
	AdmitUserTurn(
		context.Context,
		serverapi.RuntimeSubmitUserTurnRequest,
	) (serverapi.RuntimeSubmitUserTurnResponse, bool, error)
	AdmitQueuedUserInput(
		context.Context,
		serverapi.RuntimeSubmitUserTurnRequest,
	) (runtimeids.QueueItemID, bool, error)
	AdmitManualCompaction(
		context.Context,
		serverapi.RuntimeCompactContextRequest,
	) (bool, error)
}

type Service struct {
	operations *OperationOwner
	targets    TargetResolutionService
	runtimes   RuntimeOpeningService
	admissions RuntimeAdmissionService
}

func NewService(
	operations *OperationOwner,
	targets TargetResolutionService,
	runtimes RuntimeOpeningService,
	admissions RuntimeAdmissionService,
) *Service {
	return &Service{
		operations: operations,
		targets:    targets,
		runtimes:   runtimes,
		admissions: admissions,
	}
}

func (s *Service) Steer(
	ctx context.Context,
	request *chatpb.SteerRequest,
) (*chatpb.InputMutationSuccess, error) {
	if err := protoapi.Validate(request); err != nil {
		return nil, err
	}
	return s.runInputMutation(ctx, request.Target, request.Activation, inputMutationSteer)
}

func (s *Service) Queue(
	ctx context.Context,
	request *chatpb.QueueRequest,
) (*chatpb.InputMutationSuccess, error) {
	if err := protoapi.Validate(request); err != nil {
		return nil, err
	}
	return s.runInputMutation(ctx, request.Target, request.Activation, inputMutationQueue)
}

func (s *Service) Compact(
	ctx context.Context,
	request *chatpb.CompactRequest,
) (*chatpb.CompactionMutationSuccess, error) {
	if err := protoapi.Validate(request); err != nil {
		return nil, err
	}
	draft, admission, err := compactionInvocation(request.Invocation)
	if err != nil {
		return nil, err
	}
	if s == nil || s.operations == nil {
		return nil, errors.New("Chat operation owner is required")
	}
	var result *chatpb.CompactionMutationSuccess
	operation, err := s.operations.Start(ctx, func(scope OperationScope) error {
		var operationErr error
		result, operationErr = s.compact(scope, request.Target, draft, admission)
		return operationErr
	})
	if err != nil {
		return nil, err
	}
	if err := operation.Await(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) compact(
	scope OperationScope,
	targetRequest *chatpb.ChatTarget,
	draft string,
	admission runtimeinput.ManualCompactionAdmission,
) (*chatpb.CompactionMutationSuccess, error) {
	ctx := scope.Context()
	target, attachment, err := s.prepareRuntime(scope, targetRequest, draft)
	if err != nil {
		if target.SessionID.IsZero() {
			return nil, err
		}
		return compactionNotAccepted(target.SessionID, err), nil
	}
	if s.admissions == nil {
		releaseErr := scope.FinalizeAttachment(func(finalizationCtx context.Context) error {
			return attachment.Release(finalizationCtx, sessionruntime.RuntimeReleaseCloseIfIdle)
		})
		return compactionNotAcceptedInternal(
			target.SessionID,
			"release_chat_runtime",
			errors.Join(errors.New("manual-compaction admission service is required"), releaseErr),
		), nil
	}
	requestID := runtimeids.NewCompactionRequestID()
	accepted, admissionErr := s.admissions.AdmitManualCompaction(ctx, serverapi.RuntimeCompactContextRequest{
		SessionID: target.SessionID.String(),
		RequestID: requestID,
		Admission: admission,
	})
	releasePolicy := sessionruntime.RuntimeReleaseCloseIfIdle
	if accepted {
		releasePolicy = sessionruntime.RuntimeReleaseDetach
	}
	releaseErr := scope.FinalizeAttachment(func(finalizationCtx context.Context) error {
		return attachment.Release(finalizationCtx, releasePolicy)
	})
	if !accepted {
		if releaseErr != nil {
			return compactionNotAcceptedInternal(
				target.SessionID,
				"release_chat_runtime",
				errors.Join(admissionErr, releaseErr),
			), nil
		}
		return compactionNotAccepted(target.SessionID, admissionErr), nil
	}
	acceptedResult := &chatpb.CompactionAccepted{
		Request: &chatpb.CompactionRequestIdentity{Id: requestID.String()},
	}
	switch {
	case releaseErr != nil:
		acceptedResult.Diagnostic = acceptedDiagnostic(
			"release_chat_runtime",
			errors.Join(admissionErr, releaseErr),
		)
	case admissionErr != nil:
		acceptedResult.Diagnostic = acceptedDiagnostic("chat_compact", admissionErr)
	}
	return &chatpb.CompactionMutationSuccess{
		Session: &chatpb.ExistingSessionTarget{SessionId: target.SessionID.String()},
		Outcome: &chatpb.CompactionMutationSuccess_Accepted{Accepted: acceptedResult},
	}, nil
}

type inputMutationOperation string

const (
	inputMutationSteer inputMutationOperation = "chat_steer"
	inputMutationQueue inputMutationOperation = "chat_queue"
)

func (s *Service) mutateInput(
	scope OperationScope,
	targetRequest *chatpb.ChatTarget,
	activation *chatpb.Activation,
	operation inputMutationOperation,
) (*chatpb.InputMutationSuccess, error) {
	ctx := scope.Context()
	draft, input, err := activationInput(activation)
	if err != nil {
		return nil, err
	}
	target, attachment, err := s.prepareRuntime(scope, targetRequest, draft)
	if err != nil {
		if target.SessionID.IsZero() {
			return nil, err
		}
		return inputNotAccepted(target.SessionID, operation, err), nil
	}
	if s.admissions == nil {
		releaseErr := scope.FinalizeAttachment(func(finalizationCtx context.Context) error {
			return attachment.Release(finalizationCtx, sessionruntime.RuntimeReleaseCloseIfIdle)
		})
		return inputNotAccepted(
			target.SessionID,
			operation,
			errors.Join(errors.New("user-turn admission service is required"), releaseErr),
		), nil
	}
	queueItemID, accepted, admissionErr := s.admitInput(ctx, operation, serverapi.RuntimeSubmitUserTurnRequest{
		SessionID: target.SessionID.String(),
		Input:     input,
	})
	releasePolicy := sessionruntime.RuntimeReleaseCloseIfIdle
	if accepted {
		releasePolicy = sessionruntime.RuntimeReleaseDetach
	}
	releaseErr := scope.FinalizeAttachment(func(finalizationCtx context.Context) error {
		return attachment.Release(finalizationCtx, releasePolicy)
	})
	if !accepted {
		if releaseErr != nil {
			return inputNotAcceptedInternal(
				target.SessionID,
				"release_chat_runtime",
				errors.Join(admissionErr, releaseErr),
			), nil
		}
		return inputNotAccepted(target.SessionID, operation, errors.Join(admissionErr, releaseErr)), nil
	}
	if queueItemID.IsZero() {
		return nil, errors.Join(
			fmt.Errorf("accepted Chat %s Queue Item identity is required", operation),
			admissionErr,
			releaseErr,
		)
	}
	acceptedResult := &chatpb.InputAccepted{
		QueueItem: &chatpb.QueueItemIdentity{Id: queueItemID.String()},
	}
	switch {
	case releaseErr != nil:
		acceptedResult.Diagnostic = acceptedDiagnostic(
			"release_chat_runtime",
			errors.Join(admissionErr, releaseErr),
		)
	case admissionErr != nil:
		acceptedResult.Diagnostic = acceptedDiagnostic(string(operation), admissionErr)
	}
	return &chatpb.InputMutationSuccess{
		Session: &chatpb.ExistingSessionTarget{SessionId: target.SessionID.String()},
		Outcome: &chatpb.InputMutationSuccess_Accepted{Accepted: acceptedResult},
	}, nil
}

func (s *Service) runInputMutation(
	ctx context.Context,
	target *chatpb.ChatTarget,
	activation *chatpb.Activation,
	kind inputMutationOperation,
) (*chatpb.InputMutationSuccess, error) {
	if s == nil || s.operations == nil {
		return nil, errors.New("Chat operation owner is required")
	}
	var result *chatpb.InputMutationSuccess
	operation, err := s.operations.Start(ctx, func(scope OperationScope) error {
		var operationErr error
		result, operationErr = s.mutateInput(scope, target, activation, kind)
		return operationErr
	})
	if err != nil {
		return nil, err
	}
	if err := operation.Await(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) prepareRuntime(
	scope OperationScope,
	targetRequest *chatpb.ChatTarget,
	initialDraft string,
) (ResolvedTarget, RuntimeAttachment, error) {
	ctx := scope.Context()
	if s == nil || s.targets == nil {
		return ResolvedTarget{}, nil, errors.New("Chat target resolver is required")
	}
	target, err := s.targets.Resolve(ctx, TargetResolutionRequest{
		Target:       targetRequest,
		InitialDraft: &initialDraft,
	})
	if err != nil {
		return ResolvedTarget{}, nil, err
	}
	if s.runtimes == nil {
		return target, nil, errors.New("Session Runtime planner is required")
	}
	attachment, err := s.runtimes.Open(ctx, target.SessionID)
	if err != nil {
		if attachment == nil {
			return target, nil, err
		}
		releaseErr := scope.FinalizeAttachment(func(finalizationCtx context.Context) error {
			return attachment.Release(finalizationCtx, sessionruntime.RuntimeReleaseCloseIfIdle)
		})
		return target, nil, errors.Join(err, releaseErr)
	}
	if attachment == nil {
		return target, nil, errors.New("Session Runtime attachment is required")
	}
	if attachment.SessionID() != target.SessionID {
		releaseErr := scope.FinalizeAttachment(func(finalizationCtx context.Context) error {
			return attachment.Release(finalizationCtx, sessionruntime.RuntimeReleaseCloseIfIdle)
		})
		return target, nil, errors.Join(
			errors.New("Session Runtime attachment targets another Session"),
			releaseErr,
		)
	}
	return target, attachment, nil
}

func (s *Service) admitInput(
	ctx context.Context,
	operation inputMutationOperation,
	request serverapi.RuntimeSubmitUserTurnRequest,
) (runtimeids.QueueItemID, bool, error) {
	switch operation {
	case inputMutationSteer:
		response, accepted, err := s.admissions.AdmitUserTurn(ctx, request)
		if !accepted {
			return runtimeids.QueueItemID{}, false, err
		}
		queueItemID, parseErr := runtimeids.ParseQueueItemID(response.QueueItemID)
		return queueItemID, true, errors.Join(err, parseErr)
	case inputMutationQueue:
		return s.admissions.AdmitQueuedUserInput(ctx, request)
	default:
		return runtimeids.QueueItemID{}, false, fmt.Errorf("unsupported Chat input mutation %q", operation)
	}
}

func activationInput(activation *chatpb.Activation) (string, runtimeinput.Input, error) {
	if err := protoapi.Validate(activation); err != nil {
		return "", runtimeinput.Input{}, err
	}
	if text, ok := activation.Input.(*chatpb.Activation_Text); ok {
		return text.Text, runtimeinput.Text(text.Text), nil
	}
	command := activation.GetCommand()
	if command == nil {
		return "", runtimeinput.Input{}, errors.New("Chat activation selection is required")
	}
	draft := command.Token + command.SeparatorWhitespace + command.Arguments
	return draft, runtimeinput.Command(command.CatalogIdentity, command.Arguments), nil
}

func compactionInvocation(
	invocation *chatpb.CompactionInvocation,
) (string, runtimeinput.ManualCompactionAdmission, error) {
	if err := protoapi.Validate(invocation); err != nil {
		return "", runtimeinput.ManualCompactionAdmission{}, err
	}
	draft := invocation.Token + invocation.SeparatorWhitespace + invocation.RawGuidance
	normalizedGuidance := runtimeinput.NormalizePendingWorkArgument(invocation.RawGuidance)
	admission := runtimeinput.ManualCompactionAdmission{}
	if normalizedGuidance != "" {
		admission.Guidance = &normalizedGuidance
	}
	return draft, admission, nil
}

func compactionNotAccepted(
	sessionID runtimeids.SessionID,
	cause error,
) *chatpb.CompactionMutationSuccess {
	if cause == nil {
		cause = errors.New("manual-compaction admission completed without accepting work")
	}
	return &chatpb.CompactionMutationSuccess{
		Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
		Outcome: &chatpb.CompactionMutationSuccess_NotAccepted{
			NotAccepted: compactionNotAcceptedForCause(cause),
		},
	}
}

func compactionNotAcceptedInternal(
	sessionID runtimeids.SessionID,
	operation string,
	cause error,
) *chatpb.CompactionMutationSuccess {
	return &chatpb.CompactionMutationSuccess{
		Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
		Outcome: &chatpb.CompactionMutationSuccess_NotAccepted{
			NotAccepted: &chatpb.CompactionNotAccepted{
				Reason: &chatpb.CompactionNotAccepted_InternalFailure{
					InternalFailure: internalFailure(operation, cause),
				},
			},
		},
	}
}

func compactionNotAcceptedForCause(cause error) *chatpb.CompactionNotAccepted {
	switch {
	case errors.Is(cause, context.Canceled), errors.Is(cause, context.DeadlineExceeded):
		return &chatpb.CompactionNotAccepted{
			Reason: &chatpb.CompactionNotAccepted_Canceled{Canceled: &emptypb.Empty{}},
		}
	case errors.Is(cause, serverapi.ErrRuntimeUnavailable):
		return &chatpb.CompactionNotAccepted{
			Reason: &chatpb.CompactionNotAccepted_RuntimeUnavailable{
				RuntimeUnavailable: &chatpb.RuntimeUnavailableDetails{},
			},
		}
	case errors.Is(cause, serverapi.ErrPendingWorkCapacity):
		return &chatpb.CompactionNotAccepted{
			Reason: &chatpb.CompactionNotAccepted_PendingWorkCapacity{
				PendingWorkCapacity: &chatpb.PendingWorkCapacityDetails{},
			},
		}
	case errors.Is(cause, serverapi.ErrManualCompactionTooSoon):
		return &chatpb.CompactionNotAccepted{
			Reason: &chatpb.CompactionNotAccepted_TooSoon{
				TooSoon: &chatpb.ManualCompactionTooSoonDetails{},
			},
		}
	case errors.Is(cause, serverapi.ErrManualCompactionDisabled):
		return &chatpb.CompactionNotAccepted{
			Reason: &chatpb.CompactionNotAccepted_Disabled{
				Disabled: &chatpb.ManualCompactionDisabledDetails{},
			},
		}
	case errors.Is(cause, serverapi.ErrManualCompactionActive):
		return &chatpb.CompactionNotAccepted{
			Reason: &chatpb.CompactionNotAccepted_Active{
				Active: &chatpb.ManualCompactionActiveDetails{},
			},
		}
	default:
		return &chatpb.CompactionNotAccepted{
			Reason: &chatpb.CompactionNotAccepted_InternalFailure{
				InternalFailure: internalFailure("chat_compact", cause),
			},
		}
	}
}

func inputNotAccepted(
	sessionID runtimeids.SessionID,
	operation inputMutationOperation,
	cause error,
) *chatpb.InputMutationSuccess {
	if cause == nil {
		cause = errors.New("user-turn admission completed without accepting work")
	}
	return &chatpb.InputMutationSuccess{
		Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
		Outcome: &chatpb.InputMutationSuccess_NotAccepted{
			NotAccepted: inputNotAcceptedForCause(operation, cause),
		},
	}
}

func inputNotAcceptedInternal(
	sessionID runtimeids.SessionID,
	operation string,
	cause error,
) *chatpb.InputMutationSuccess {
	return &chatpb.InputMutationSuccess{
		Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
		Outcome: &chatpb.InputMutationSuccess_NotAccepted{
			NotAccepted: &chatpb.InputNotAccepted{
				Reason: &chatpb.InputNotAccepted_InternalFailure{
					InternalFailure: internalFailure(operation, cause),
				},
			},
		},
	}
}

func inputNotAcceptedForCause(
	operation inputMutationOperation,
	cause error,
) *chatpb.InputNotAccepted {
	switch {
	case errors.Is(cause, context.Canceled), errors.Is(cause, context.DeadlineExceeded):
		return &chatpb.InputNotAccepted{
			Reason: &chatpb.InputNotAccepted_Canceled{Canceled: &emptypb.Empty{}},
		}
	case errors.Is(cause, serverapi.ErrRuntimeUnavailable):
		return &chatpb.InputNotAccepted{
			Reason: &chatpb.InputNotAccepted_RuntimeUnavailable{
				RuntimeUnavailable: &chatpb.RuntimeUnavailableDetails{},
			},
		}
	case errors.Is(cause, serverapi.ErrPendingWorkCapacity):
		return &chatpb.InputNotAccepted{
			Reason: &chatpb.InputNotAccepted_PendingWorkCapacity{
				PendingWorkCapacity: &chatpb.PendingWorkCapacityDetails{},
			},
		}
	}
	var promptErr *serverapi.PromptCommandError
	if errors.As(cause, &promptErr) {
		switch promptErr.Kind {
		case serverapi.PromptCommandErrorKindCatalogRead:
			return &chatpb.InputNotAccepted{
				Reason: &chatpb.InputNotAccepted_PromptCatalogRead{
					PromptCatalogRead: &promptcommandpb.CatalogReadDetails{Command: promptErr.Command},
				},
			}
		case serverapi.PromptCommandErrorKindCommandNotFound:
			if promptErr.Command != nil {
				return &chatpb.InputNotAccepted{
					Reason: &chatpb.InputNotAccepted_PromptCommandNotFound{
						PromptCommandNotFound: &promptcommandpb.CommandNotFoundDetails{Command: *promptErr.Command},
					},
				}
			}
		case serverapi.PromptCommandErrorKindCommandRead:
			if promptErr.Command != nil {
				return &chatpb.InputNotAccepted{
					Reason: &chatpb.InputNotAccepted_PromptCommandRead{
						PromptCommandRead: &promptcommandpb.CommandReadDetails{Command: *promptErr.Command},
					},
				}
			}
		}
	}
	return &chatpb.InputNotAccepted{
		Reason: &chatpb.InputNotAccepted_InternalFailure{
			InternalFailure: internalFailure(string(operation), cause),
		},
	}
}

func acceptedDiagnostic(operation string, cause error) *chatpb.AcceptedDiagnostic {
	return &chatpb.AcceptedDiagnostic{
		Detail: &chatpb.AcceptedDiagnostic_InternalFailure{
			InternalFailure: internalFailure(operation, cause),
		},
	}
}

func internalFailure(operation string, cause error) *sharedpb.InternalFailureDetails {
	details := &sharedpb.InternalFailureDetails{}
	if operation != "" {
		details.Operation = &operation
	}
	if cause != nil {
		message := cause.Error()
		details.Cause = &message
	}
	return details
}
