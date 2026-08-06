package runtime

import (
	"fmt"
	"strings"

	"core/server/llm"
	"core/shared/transcript"
)

func (s *defaultStepExecutor) reconcileReasoning(stepID string, entries []llm.ReasoningEntry) error {
	_, provisional := s.engine.transcriptRuntimeState().ReasoningSnapshot()
	type provisionalTrace struct {
		trace   TranscriptReasoningTraceState
		ordinal int
	}
	byCoordinate := make(map[transcriptReasoningCoordinate]provisionalTrace, len(provisional))
	for ordinal, trace := range provisional {
		if trace.Source.OutputIndex == nil || trace.Source.PartIndex == nil {
			return fmt.Errorf("provisional reasoning trace has no source coordinate")
		}
		key := transcriptReasoningCoordinate{
			output: *trace.Source.OutputIndex,
			part:   *trace.Source.PartIndex,
		}
		byCoordinate[key] = provisionalTrace{trace: trace, ordinal: ordinal}
	}
	seenCoordinates := make(map[transcriptReasoningCoordinate]struct{}, len(entries))
	seenAliases := make(map[string]transcriptReasoningCoordinate, len(entries))
	lastMatchedOrdinal := -1
	for _, entry := range entries {
		if strings.TrimSpace(entry.Text) == "" {
			continue
		}
		coordinate, correlated, coordinateErr := reasoningCoordinateFromEntry(entry)
		if coordinateErr != nil {
			return coordinateErr
		}
		if entry.ItemIdentity != nil {
			if err := entry.ItemIdentity.Validate(); err != nil {
				return err
			}
		}
		if correlated {
			if _, duplicate := seenCoordinates[coordinate]; duplicate {
				return fmt.Errorf("completed reasoning response repeats source coordinate output=%d part=%d", coordinate.output, coordinate.part)
			}
			seenCoordinates[coordinate] = struct{}{}
			trace, ok := byCoordinate[coordinate]
			if ok {
				if trace.ordinal < lastMatchedOrdinal {
					return fmt.Errorf("completed reasoning response reorders provisional source coordinates")
				}
				lastMatchedOrdinal = trace.ordinal
			}
			if entry.ItemIdentity != nil {
				alias, err := llm.ReasoningItemIdentityAlias(*entry.ItemIdentity)
				if err != nil {
					return err
				}
				if existing, ok := seenAliases[alias]; ok && existing != coordinate {
					return fmt.Errorf("completed reasoning response aliases one provider item to multiple source coordinates")
				}
				seenAliases[alias] = coordinate
			}
			if !ok {
				if err := s.persistCompletedOnlyReasoning(stepID, entry.Text); err != nil {
					return err
				}
				continue
			}
			if entry.ItemIdentity != nil {
				if err := s.engine.transcriptRuntimeState().ObserveReasoningItemIdentity(stepID, entry.SourceCoordinate, entry.ItemIdentity); err != nil {
					return err
				}
			}
			identity := cloneTranscriptReasoningTraceIdentity(&trace.trace.Identity)
			durationMs, durationErr := s.engine.transcriptRuntimeState().ReasoningDurationMs(stepID, entry.SourceCoordinate)
			if durationErr != nil {
				return durationErr
			}
			receipt, err := s.engine.steerWithCommitReceipt(
				stepID,
				steerReasoningLocalEntryIntent(storedLocalEntry{
					Visibility: transcript.EntryVisibilityDetail,
					Role:       string(transcript.EntryRoleReasoning),
					Text:       entry.Text,
					DurationMs: durationMs,
				}, *identity),
			)
			if receipt.Committed {
				if consumeErr := s.engine.transcriptRuntimeState().ConsumeReasoningTrace(stepID, entry.SourceCoordinate); consumeErr != nil {
					return consumeErr
				}
			}
			if err != nil {
				return err
			}
			if !receipt.Committed {
				return fmt.Errorf("reasoning trace persistence returned without a durable commit")
			}
			continue
		}
		if err := s.persistCompletedOnlyReasoning(stepID, entry.Text); err != nil {
			return err
		}
	}
	_, remaining := s.engine.transcriptRuntimeState().ReasoningSnapshot()
	if len(remaining) != 0 {
		return fmt.Errorf("accepted response left provisional reasoning traces unresolved")
	}
	return nil
}

func (s *defaultStepExecutor) resolveReasoningDisposition(
	stepID string,
	next completedResponseNext,
	entries []llm.ReasoningEntry,
) error {
	switch next {
	case completedResponseNextExternalWorkflowTerminal,
		completedResponseNextWorkflowPreflightRejected:
		return s.resetProvisionalReasoning(stepID)
	case completedResponseNextAccepted,
		completedResponseNextFinalAnswerToolsTerminal:
		return s.reconcileReasoning(stepID, entries)
	default:
		return fmt.Errorf("completed response produced invalid reasoning disposition")
	}
}

func (s *defaultStepExecutor) resetProvisionalReasoning(stepID string) error {
	_, traces := s.engine.transcriptRuntimeState().ReasoningSnapshot()
	if len(traces) == 0 {
		return nil
	}
	return s.engine.steer(stepID, steerResetReasoningStateIntent())
}

func (s *defaultStepExecutor) persistCompletedOnlyReasoning(stepID, text string) error {
	receipt, err := s.engine.steerWithCommitReceipt(
		stepID,
		steerLocalEntryIntent(storedLocalEntry{
			Visibility: transcript.EntryVisibilityDetail,
			Role:       string(transcript.EntryRoleReasoning),
			Text:       text,
		}),
	)
	if err != nil {
		return err
	}
	if !receipt.Committed {
		return fmt.Errorf("completed-only reasoning persistence returned without a durable commit")
	}
	return nil
}

func reasoningCoordinateFromEntry(entry llm.ReasoningEntry) (transcriptReasoningCoordinate, bool, error) {
	if entry.SourceCoordinate == nil {
		return transcriptReasoningCoordinate{}, false, nil
	}
	if err := entry.SourceCoordinate.Validate(); err != nil {
		return transcriptReasoningCoordinate{}, false, fmt.Errorf("completed reasoning entry has invalid source coordinate: %w", err)
	}
	return transcriptReasoningCoordinate{
		output: *entry.SourceCoordinate.OutputIndex,
		part:   *entry.SourceCoordinate.PartIndex,
	}, true, nil
}
