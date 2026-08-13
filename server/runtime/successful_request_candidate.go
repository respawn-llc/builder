package runtime

import (
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/shared/transcript"
)

type successfulRequestCandidate struct {
	response                llm.Response
	requestedModel          string
	estimatedProviderTokens int
}

func (e *Engine) commitAcceptedResponseCandidate(stepID string, candidate successfulRequestCandidate) error {
	var warningErr error
	if servedModel := candidate.response.ServedModel; servedModel != nil {
		requested := strings.TrimSpace(candidate.requestedModel)
		served := strings.TrimSpace(*servedModel)
		if requested != "" && served != "" && requested != served {
			visibility := transcript.EntryVisibilityDetail
			if e.cfg.Debug {
				visibility = transcript.EntryVisibilityOngoing
			}
			receipt, err := e.steerWithCommitReceipt(stepID, steerLocalEntryIntent(storedLocalEntry{
				Visibility: visibility,
				Role:       string(transcript.EntryRoleWarning),
				ProviderModelMismatch: &transcript.ProviderModelMismatchNotice{
					RequestedModel: requested,
					ServedModel:    served,
				},
			}))
			warningErr = err
			if !receipt.Committed && warningErr == nil {
				warningErr = fmt.Errorf("provider-model mismatch warning persistence returned without a durable commit")
			}
		}
	}
	_, usageErr := e.recordLastUsageWithBaseline(candidate.response.Usage, candidate.estimatedProviderTokens)
	return errors.Join(warningErr, usageErr)
}

func newSuccessfulRequestCandidate(request llm.Request, response llm.Response) successfulRequestCandidate {
	fullEstimate := estimateItemsTokens(request.Items)
	baseline := fullEstimate
	if !response.ReasoningIncluded {
		baseline -= estimateItemsTokens(pastReasoningBeforeLatestKentInstructionBoundary(request.Items))
		if baseline < 0 {
			baseline = 0
		}
	}
	return successfulRequestCandidate{
		response:                response,
		requestedModel:          request.Model,
		estimatedProviderTokens: baseline,
	}
}
