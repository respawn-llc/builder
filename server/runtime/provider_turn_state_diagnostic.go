package runtime

import (
	"log/slog"

	"core/server/llm"
)

func (e *Engine) publishProviderTurnStateDiagnostics(
	stepID string,
	dispatch *llm.CodexDispatchContext,
	published map[llm.CodexTurnStateDiagnosticCategory]struct{},
) {
	if e == nil || dispatch == nil {
		return
	}
	for _, category := range dispatch.TurnStateDiagnostics() {
		if _, exists := published[category]; exists {
			continue
		}
		var kind EventKind
		switch category {
		case llm.CodexTurnStateDiagnosticInvalid:
			kind = EventProviderTurnStateInvalid
		default:
			panic("unknown provider turn-state diagnostic category")
		}
		if err := e.steer(stepID, steerEventIntent(Event{Kind: kind, StepID: &stepID})); err != nil {
			slog.Error(
				"publish provider turn-state diagnostic",
				"category", category,
				"step_id", stepID,
				"error", err,
			)
			continue
		}
		published[category] = struct{}{}
	}
}
