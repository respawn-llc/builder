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

type UserTurnAdmissionService interface {
	AdmitUserTurn(
		context.Context,
		serverapi.RuntimeSubmitUserTurnRequest,
	) (serverapi.RuntimeSubmitUserTurnResponse, bool, error)
}

type Service struct {
	targets    TargetResolutionService
	runtimes   RuntimeOpeningService
	admissions UserTurnAdmissionService
}

func NewService(
	targets TargetResolutionService,
	runtimes RuntimeOpeningService,
	admissions UserTurnAdmissionService,
) *Service {
	return &Service{
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
	if s == nil || s.targets == nil {
		return nil, errors.New("Chat target resolver is required")
	}
	draft, input, err := activationInput(request.Activation)
	if err != nil {
		return nil, err
	}
	target, err := s.targets.Resolve(ctx, TargetResolutionRequest{
		Target:       request.Target,
		InitialDraft: &draft,
	})
	if err != nil {
		return nil, err
	}
	if s.runtimes == nil {
		return inputNotAccepted(target.SessionID, errors.New("Session Runtime planner is required")), nil
	}
	attachment, err := s.runtimes.Open(ctx, target.SessionID)
	if err != nil {
		return inputNotAccepted(target.SessionID, err), nil
	}
	if attachment == nil {
		return inputNotAccepted(target.SessionID, errors.New("Session Runtime attachment is required")), nil
	}
	if attachment.SessionID() != target.SessionID {
		releaseErr := attachment.Release(context.WithoutCancel(ctx), sessionruntime.RuntimeReleaseCloseIfIdle)
		return inputNotAccepted(
			target.SessionID,
			errors.Join(errors.New("Session Runtime attachment targets another Session"), releaseErr),
		), nil
	}
	if s.admissions == nil {
		releaseErr := attachment.Release(context.WithoutCancel(ctx), sessionruntime.RuntimeReleaseCloseIfIdle)
		return inputNotAccepted(
			target.SessionID,
			errors.Join(errors.New("user-turn admission service is required"), releaseErr),
		), nil
	}
	response, accepted, admissionErr := s.admissions.AdmitUserTurn(ctx, serverapi.RuntimeSubmitUserTurnRequest{
		SessionID: target.SessionID.String(),
		Input:     input,
	})
	releasePolicy := sessionruntime.RuntimeReleaseCloseIfIdle
	if accepted {
		releasePolicy = sessionruntime.RuntimeReleaseDetach
	}
	releaseErr := attachment.Release(context.WithoutCancel(ctx), releasePolicy)
	if !accepted {
		if releaseErr != nil {
			return inputNotAcceptedInternal(
				target.SessionID,
				"release_chat_runtime",
				errors.Join(admissionErr, releaseErr),
			), nil
		}
		return inputNotAccepted(target.SessionID, errors.Join(admissionErr, releaseErr)), nil
	}
	queueItemID, parseErr := runtimeids.ParseQueueItemID(response.QueueItemID)
	if parseErr != nil {
		return nil, errors.Join(
			fmt.Errorf("accepted Chat Steer Queue Item: %w", parseErr),
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
		acceptedResult.Diagnostic = acceptedDiagnostic("chat_steer", admissionErr)
	}
	return &chatpb.InputMutationSuccess{
		Session: &chatpb.ExistingSessionTarget{SessionId: target.SessionID.String()},
		Outcome: &chatpb.InputMutationSuccess_Accepted{Accepted: acceptedResult},
	}, nil
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

func inputNotAccepted(
	sessionID runtimeids.SessionID,
	cause error,
) *chatpb.InputMutationSuccess {
	if cause == nil {
		cause = errors.New("user-turn admission completed without accepting work")
	}
	return &chatpb.InputMutationSuccess{
		Session: &chatpb.ExistingSessionTarget{SessionId: sessionID.String()},
		Outcome: &chatpb.InputMutationSuccess_NotAccepted{
			NotAccepted: inputNotAcceptedForCause(cause),
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

func inputNotAcceptedForCause(cause error) *chatpb.InputNotAccepted {
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
			InternalFailure: internalFailure("chat_steer", cause),
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
