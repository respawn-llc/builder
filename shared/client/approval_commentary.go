package client

import (
	"errors"
	"strings"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type ApprovalCommentaryEffectKind string

const (
	ApprovalCommentaryEffectRuntimeInput ApprovalCommentaryEffectKind = "runtime_input"
	ApprovalCommentaryEffectApproval     ApprovalCommentaryEffectKind = "approval"
)

type ApprovalCommentaryEffect struct {
	Kind       ApprovalCommentaryEffectKind
	Commentary string
}

func NewRuntimeUserTurnRequest(sessionID, text string) serverapi.RuntimeSubmitUserTurnRequest {
	clientRequestID := runtimeids.NewRuntimeClientRequestID()
	return serverapi.RuntimeSubmitUserTurnRequest{
		ClientRequestID: clientRequestID.String(),
		SessionID:       sessionID,
		Text:            text,
		OperationRef: clientui.RuntimeOperationRef{
			Kind:            clientui.RuntimeOperationKindSubmit,
			ClientRequestID: clientRequestID,
		},
		PreSubmitCompactionOperationRef: clientui.RuntimeOperationRef{
			Kind:            clientui.RuntimeOperationKindPreSubmitCompact,
			ClientRequestID: runtimeids.NewRuntimeClientRequestID(),
		},
	}
}

// PlanApprovalCommentary describes the client-side ordering for an approval
// answer. It contains no transport requests or generated identifiers.
func PlanApprovalCommentary(decision clientui.ApprovalDecision, commentary string) ([]ApprovalCommentaryEffect, error) {
	switch decision {
	case clientui.ApprovalDecisionDeny:
		return []ApprovalCommentaryEffect{
			{Kind: ApprovalCommentaryEffectApproval, Commentary: commentary},
		}, nil
	case clientui.ApprovalDecisionAllowOnce, clientui.ApprovalDecisionAllowSession:
		effects := make([]ApprovalCommentaryEffect, 0, 2)
		if strings.TrimSpace(commentary) != "" {
			effects = append(effects, ApprovalCommentaryEffect{
				Kind:       ApprovalCommentaryEffectRuntimeInput,
				Commentary: commentary,
			})
		}
		effects = append(effects, ApprovalCommentaryEffect{Kind: ApprovalCommentaryEffectApproval})
		return effects, nil
	default:
		return nil, errors.New("approval decision is invalid")
	}
}
