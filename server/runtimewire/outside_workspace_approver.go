package runtimewire

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	actionVerb     string
	mu             sync.Mutex
	sessionAllowed bool
}

func NewOutsideWorkspaceApprover(broker *tools.AskQuestionBroker, actionVerb string) *OutsideWorkspaceApprover {
	verb := strings.TrimSpace(actionVerb)
	if verb == "" {
		verb = "accessing"
	}
	return &OutsideWorkspaceApprover{broker: broker, actionVerb: verb}
}

func (a *OutsideWorkspaceApprover) Approve(ctx context.Context, req tools.FileAccessRequest) (tools.FileAccessApproval, error) {
	a.mu.Lock()
	if a.sessionAllowed {
		a.mu.Unlock()
		return tools.FileAccessApproval{Kind: tools.FileAccessApprovalSessionCached}, nil
	}
	a.mu.Unlock()

	request := tools.AskQuestionRequest{
		Question: fmt.Sprintf("Allow %s %s (outside workspace dir)?", a.actionVerb, req.ResolvedPath),
		Approval: true,
		ApprovalOptions: []tools.AskQuestionApprovalOption{
			{Decision: tools.AskQuestionApprovalDecisionAllowOnce, Label: OutsideWorkspaceAllowOnceSuggestion},
			{Decision: tools.AskQuestionApprovalDecisionAllowSession, Label: OutsideWorkspaceAllowSessionSuggestion},
			{Decision: tools.AskQuestionApprovalDecisionDeny, Label: OutsideWorkspaceDenySuggestion},
		},
	}
	if identity, identityErr := tools.ExecutionIdentityFromContext(ctx); identityErr == nil {
		request.RunID = identity.RunID
		request.StepID = identity.StepID
	}
	resp, err := a.broker.Ask(ctx, request)
	if err != nil {
		return tools.FileAccessApproval{Kind: tools.FileAccessApprovalDeny}, err
	}

	approval, err := OutsideWorkspaceApprovalFromResolution(resp)
	if err != nil {
		return tools.FileAccessApproval{Kind: tools.FileAccessApprovalDeny}, err
	}
	if approval.Kind == tools.FileAccessApprovalAllowSession {
		a.mu.Lock()
		a.sessionAllowed = true
		a.mu.Unlock()
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
