package runtime

import (
	"context"
	"fmt"

	"core/server/llm"
)

type defaultPhaseProtocol struct {
	engine *Engine
}

func (p *defaultPhaseProtocol) EnabledForModel(ctx context.Context) bool {
	e := p.engine
	state := e.phaseProtocolState()
	if enabled, resolved := state.Snapshot(); resolved {
		return enabled
	}

	enabled := false
	if caps, err := e.providerCapabilities(ctx); err == nil {
		enabled = caps.SupportsResponsesAPI && caps.IsOpenAIFirstParty
	}

	return state.Resolve(enabled)
}

func (e *Engine) phaseProtocolState() *phaseProtocolState {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.phaseState == nil {
		e.phaseState = newPhaseProtocolState()
	}
	return e.phaseState
}

func (p *defaultPhaseProtocol) Apply(ctx context.Context, resp llm.Response, assistant llm.Message, localToolCalls []llm.ToolCall, hostedToolExecutions []hostedToolExecution) (phaseProtocolTurn, error) {
	phaseProtocolEnabled := p.EnabledForModel(ctx)
	structuredPhaseProtocol := shouldTreatMissingAssistantPhaseAsCommentary(resp)
	effectivePhase := resp.ProviderPhase
	providerPhase := effectivePhase.Value()
	if effectivePhase != nil {
		if providerPhase != nil {
			if assistant.Phase != *providerPhase {
				return phaseProtocolTurn{}, fmt.Errorf(
					"assistant phase projection mismatch: provider phase %q, assistant phase %q",
					*providerPhase,
					assistant.Phase,
				)
			}
		} else if assistant.Phase != "" {
			return phaseProtocolTurn{}, fmt.Errorf(
				"assistant phase projection mismatch: provider phase absent, assistant phase %q",
				assistant.Phase,
			)
		}
	} else {
		switch assistant.Phase {
		case llm.MessagePhaseCommentary:
			effectivePhase = llm.CommentaryProviderPhase()
		case llm.MessagePhaseFinal:
			effectivePhase = llm.FinalProviderPhase()
		}
	}
	effectivePhasePresent := effectivePhase.Value() != nil
	enforcePhaseProtocol := phaseProtocolEnabled && (structuredPhaseProtocol || effectivePhasePresent)
	missingAssistantPhase := enforcePhaseProtocol && effectivePhase.IsAbsent()
	if missingAssistantPhase {
		effectivePhase = llm.CommentaryProviderPhase()
		assistant.Phase = llm.MessagePhaseCommentary
	}
	if len(localToolCalls) > 0 {
		assistant.ToolCalls = append([]llm.ToolCall(nil), localToolCalls...)
	}
	if len(hostedToolExecutions) > 0 {
		for _, hosted := range hostedToolExecutions {
			assistant.ToolCalls = append(assistant.ToolCalls, hosted.Call)
		}
	}
	if len(resp.ReasoningItems) > 0 && len(assistant.ReasoningItems) == 0 {
		assistant.ReasoningItems = append([]llm.ReasoningItem(nil), resp.ReasoningItems...)
	}
	return phaseProtocolTurn{
		Assistant:             assistant,
		EffectivePhase:        effectivePhase,
		LocalToolCalls:        localToolCalls,
		HostedToolExecutions:  hostedToolExecutions,
		EnforcePhaseProtocol:  enforcePhaseProtocol,
		MissingAssistantPhase: missingAssistantPhase,
	}, nil
}

func shouldTreatMissingAssistantPhaseAsCommentary(resp llm.Response) bool {
	for _, item := range resp.OutputItems {
		if item.Type == llm.ResponseItemTypeMessage && item.Role == llm.RoleAssistant {
			return true
		}
	}
	return false
}
