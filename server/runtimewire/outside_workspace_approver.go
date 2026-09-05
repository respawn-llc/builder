package runtimewire

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"core/server/tools"
)

const (
	OutsideWorkspaceAllowOnceSuggestion    = "Allow once"
	OutsideWorkspaceAllowSessionSuggestion = "Allow for this session"
	OutsideWorkspaceDenySuggestion         = "Deny"
)

type OutsideWorkspaceApprover struct {
	broker         *tools.AskQuestionBroker
	mu             sync.Mutex
	sessionAllowed bool
}

func NewOutsideWorkspaceApprover(broker *tools.AskQuestionBroker) *OutsideWorkspaceApprover {
	return &OutsideWorkspaceApprover{broker: broker}
}

func (a *OutsideWorkspaceApprover) Approve(ctx context.Context, req tools.FileAccessApprovalRequest) (tools.FileAccessApproval, error) {
	a.mu.Lock()
	if a.sessionAllowed {
		a.mu.Unlock()
		return tools.FileAccessApproval{Kind: tools.FileAccessApprovalSessionCached}, nil
	}
	a.mu.Unlock()

	targets := append([]tools.FileAccessTarget(nil), req.Targets...)
	if len(targets) == 0 {
		return tools.FileAccessApproval{}, errors.New("outside-workspace Approval requires at least one target")
	}
	identity, err := tools.ExecutionIdentityFromContext(ctx)
	if err != nil {
		return tools.FileAccessApproval{}, fmt.Errorf("outside-workspace Approval owner: %w", err)
	}
	var consumerOnce sync.Once
	var consumerErr error
	request := tools.AskQuestionRequest{
		Approval:      true,
		AccessTargets: targets,
		RunID:         identity.RunID,
		StepID:        identity.StepID,
		ToolCallID:    string(identity.ToolCallID),
		ApprovalOptions: []tools.AskQuestionApprovalOption{
			{Decision: tools.AskQuestionApprovalDecisionAllowOnce, Label: OutsideWorkspaceAllowOnceSuggestion},
			{Decision: tools.AskQuestionApprovalDecisionAllowSession, Label: OutsideWorkspaceAllowSessionSuggestion},
			{Decision: tools.AskQuestionApprovalDecisionDeny, Label: OutsideWorkspaceDenySuggestion},
		},
		ApprovalConsumer: func(answer tools.AskQuestionApproval) error {
			consumerOnce.Do(func() {
				approval, err := OutsideWorkspaceApprovalFromResolution(answer)
				consumerErr = err
				if err == nil && approval.Kind == tools.FileAccessApprovalAllowSession {
					a.mu.Lock()
					a.sessionAllowed = true
					a.mu.Unlock()
				}
			})
			return consumerErr
		},
	}
	resp, err := a.broker.Ask(ctx, request)
	if err != nil {
		return tools.FileAccessApproval{Kind: tools.FileAccessApprovalDeny}, err
	}

	approval, err := OutsideWorkspaceApprovalFromResolution(resp)
	if err != nil {
		return tools.FileAccessApproval{Kind: tools.FileAccessApprovalDeny}, err
	}
	return approval, nil
}

func OutsideWorkspaceApprovalFromResolution(
	resolution tools.AskQuestionResolution,
) (tools.FileAccessApproval, error) {
	if err := tools.ValidateAskQuestionResolutionShape(resolution); err != nil {
		return tools.FileAccessApproval{}, fmt.Errorf("validate approval resolution: %w", err)
	}
	answer, ok := resolution.(tools.AskQuestionApproval)
	if !ok {
		return tools.FileAccessApproval{}, errors.New("missing approval payload")
	}
	approval := tools.FileAccessApproval{Commentary: answer.Commentary}
	switch answer.Decision {
	case tools.AskQuestionApprovalDecisionAllowOnce:
		approval.Kind = tools.FileAccessApprovalAllowOnce
	case tools.AskQuestionApprovalDecisionAllowSession:
		approval.Kind = tools.FileAccessApprovalAllowSession
	case tools.AskQuestionApprovalDecisionDeny:
		approval.Kind = tools.FileAccessApprovalDeny
	default:
		return tools.FileAccessApproval{}, fmt.Errorf("unsupported approval decision %q", answer.Decision)
	}
	return approval, nil
}
