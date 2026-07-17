package runtimewire

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	askquestion "core/server/tools"
	patchtool "core/server/tools/patch"
)

const (
	OutsideWorkspaceAllowOnceSuggestion    = "Allow once"
	OutsideWorkspaceAllowSessionSuggestion = "Allow for this session"
	OutsideWorkspaceDenySuggestion         = "Deny"
)

type OutsideWorkspaceApprover struct {
	broker         *askquestion.AskQuestionBroker
	actionVerb     string
	mu             sync.Mutex
	sessionAllowed bool
}

func NewOutsideWorkspaceApprover(broker *askquestion.AskQuestionBroker, actionVerb string) *OutsideWorkspaceApprover {
	verb := strings.TrimSpace(actionVerb)
	if verb == "" {
		verb = "accessing"
	}
	return &OutsideWorkspaceApprover{broker: broker, actionVerb: verb}
}

func (a *OutsideWorkspaceApprover) Approve(ctx context.Context, req patchtool.OutsideWorkspaceRequest) (patchtool.OutsideWorkspaceApproval, error) {
	a.mu.Lock()
	if a.sessionAllowed {
		a.mu.Unlock()
		return patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionAllowSession}, nil
	}
	a.mu.Unlock()

	request := askquestion.AskQuestionRequest{
		Question: fmt.Sprintf("Allow %s %s (outside workspace dir)?", a.actionVerb, req.ResolvedPath),
		Approval: true,
		ApprovalOptions: []askquestion.AskQuestionApprovalOption{
			{Decision: askquestion.AskQuestionApprovalDecisionAllowOnce, Label: OutsideWorkspaceAllowOnceSuggestion},
			{Decision: askquestion.AskQuestionApprovalDecisionAllowSession, Label: OutsideWorkspaceAllowSessionSuggestion},
			{Decision: askquestion.AskQuestionApprovalDecisionDeny, Label: OutsideWorkspaceDenySuggestion},
		},
	}
	if identity, identityErr := askquestion.ExecutionIdentityFromContext(ctx); identityErr == nil {
		request.RunID = identity.RunID
		request.StepID = identity.StepID
	}
	resp, err := a.broker.Ask(ctx, request)
	if err != nil {
		return patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionDeny}, err
	}

	approval, err := OutsideWorkspaceApprovalFromResponse(resp)
	if err != nil {
		return patchtool.OutsideWorkspaceApproval{Decision: patchtool.OutsideWorkspaceDecisionDeny}, err
	}
	if approval.Decision == patchtool.OutsideWorkspaceDecisionAllowSession {
		a.mu.Lock()
		a.sessionAllowed = true
		a.mu.Unlock()
	}
	return approval, nil
}

func OutsideWorkspaceApprovalFromResponse(resp askquestion.AskQuestionResponse) (patchtool.OutsideWorkspaceApproval, error) {
	payload := resp.Approval
	if payload == nil {
		return patchtool.OutsideWorkspaceApproval{}, errors.New("missing approval payload")
	}
	approval := patchtool.OutsideWorkspaceApproval{Commentary: strings.TrimSpace(payload.Commentary)}
	switch payload.Decision {
	case askquestion.AskQuestionApprovalDecisionAllowOnce:
		approval.Decision = patchtool.OutsideWorkspaceDecisionAllowOnce
	case askquestion.AskQuestionApprovalDecisionAllowSession:
		approval.Decision = patchtool.OutsideWorkspaceDecisionAllowSession
	case askquestion.AskQuestionApprovalDecisionDeny:
		approval.Decision = patchtool.OutsideWorkspaceDecisionDeny
	default:
		return patchtool.OutsideWorkspaceApproval{}, fmt.Errorf("unsupported approval decision %q", payload.Decision)
	}
	return approval, nil
}
