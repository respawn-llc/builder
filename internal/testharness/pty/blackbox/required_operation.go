package blackbox

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

const (
	maxRequiredOperationPayload = 64 * 1024
	maxModelOperations          = 128
)

type RequiredOperation struct {
	ID                    uuid.UUID
	Route                 Route
	Probe                 *string
	DeveloperMessageCount *int
	Outcome               Outcome
	Output                *string
	ResponsePhase         *ResponsePhase
	SessionCacheKey       bool
}

type ResponsePhase string

const (
	ResponsePhaseAbsent     ResponsePhase = "absent"
	ResponsePhaseCommentary ResponsePhase = "commentary"
	ResponsePhaseFinal      ResponsePhase = "final_answer"
)

func NewResponsePhase(phase ResponsePhase) *ResponsePhase {
	return &phase
}

type Outcome string

const (
	OutcomeJSON            Outcome = "json"
	OutcomeStream          Outcome = "stream"
	OutcomeProviderFailure Outcome = "provider_failure"
	OutcomeHoldSSE         Outcome = "hold_sse"
)

type Route string

const (
	RouteResponses   Route = "responses"
	RouteCompact     Route = "compact"
	RouteInputTokens Route = "input_tokens"
	RouteModel       Route = "model_metadata"
)

func (operation RequiredOperation) Validate() error {
	if operation.ID == uuid.Nil || operation.ID.Version() != 4 {
		return errors.New("model operation id must be UUIDv4")
	}
	switch operation.Route {
	case RouteResponses, RouteCompact, RouteInputTokens, RouteModel:
	default:
		return fmt.Errorf("unsupported model route %q", operation.Route)
	}
	if operation.Probe != nil && len(*operation.Probe) > maxRequiredOperationPayload {
		return errors.New("model probe exceeds limit")
	}
	if operation.Output != nil && len(*operation.Output) > maxRequiredOperationPayload {
		return errors.New("model payload exceeds limit")
	}
	if operation.DeveloperMessageCount != nil && *operation.DeveloperMessageCount < 0 {
		return errors.New("developer message count must be nonnegative")
	}
	if operation.Route != RouteResponses && (operation.Probe != nil || operation.DeveloperMessageCount != nil || operation.Output != nil || operation.ResponsePhase != nil || operation.SessionCacheKey || operation.Outcome == OutcomeStream || operation.Outcome == OutcomeHoldSSE) {
		return errors.New("only responses operations may declare input shape, output, response phase, session cache key, stream, or hold outcome")
	}
	emitsAssistantMessage := operation.Route == RouteResponses && (operation.Output != nil || operation.Outcome == OutcomeStream)
	if emitsAssistantMessage {
		if operation.ResponsePhase == nil {
			return errors.New("responses operation emitting an assistant message requires response_phase")
		}
		switch *operation.ResponsePhase {
		case ResponsePhaseAbsent, ResponsePhaseCommentary, ResponsePhaseFinal:
		default:
			return errors.New("responses operation emitting an assistant message requires a valid response_phase")
		}
	} else if operation.ResponsePhase != nil {
		return errors.New("response_phase requires an emitted assistant message")
	}
	if operation.Probe != nil {
		probe, err := uuid.Parse(*operation.Probe)
		if err != nil || probe == uuid.Nil || probe.Version() != 4 {
			return errors.New("response probe must be UUIDv4")
		}
	}
	switch operation.Outcome {
	case OutcomeJSON, OutcomeStream, OutcomeProviderFailure, OutcomeHoldSSE:
	default:
		return fmt.Errorf("unsupported model outcome %q", operation.Outcome)
	}
	return nil
}
