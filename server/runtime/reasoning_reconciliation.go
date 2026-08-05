package runtime

import (
	"fmt"
	"strings"

	"core/server/llm"
	"core/shared/transcript"
)

func (s *defaultStepExecutor) reconcileReasoning(stepID string, entries []llm.ReasoningEntry) error {
	_, provisional := s.engine.transcriptRuntimeState().ReasoningSnapshot()
	byCoordinate := make(map[transcriptReasoningCoordinate]TranscriptReasoningTraceState, len(provisional))
	for _, trace := range provisional {
		if trace.Source.OutputIndex == nil || trace.Source.PartIndex == nil {
			return fmt.Errorf("provisional reasoning trace has no source coordinate")
		}
		key := transcriptReasoningCoordinate{
			output: *trace.Source.OutputIndex,
			part:   *trace.Source.PartIndex,
		}
		byCoordinate[key] = trace
	}
	seenCoordinates := make(map[transcriptReasoningCoordinate]struct{}, len(entries))
	seenAliases := make(map[string]transcriptReasoningCoordinate, len(entries))
	lastMatched := transcriptReasoningCoordinate{}
	hasLastMatched := false
	for _, entry := range entries {
		if strings.TrimSpace(entry.Text) == "" {
			continue
		}
		coordinate, correlated, coordinateErr := reasoningCoordinateFromEntry(entry)
		if coordinateErr != nil {
			return coordinateErr
		}
		if correlated {
			if _, duplicate := seenCoordinates[coordinate]; duplicate {
				return fmt.Errorf("completed reasoning response repeats source coordinate output=%d part=%d", coordinate.output, coordinate.part)
			}
			seenCoordinates[coordinate] = struct{}{}
			if hasLastMatched && coordinate.output < lastMatched.output ||
				(hasLastMatched && coordinate.output == lastMatched.output && coordinate.part < lastMatched.part) {
				return fmt.Errorf("completed reasoning response reorders source coordinates")
			}
			lastMatched = coordinate
			hasLastMatched = true
			trace, ok := byCoordinate[coordinate]
			if !ok {
				return fmt.Errorf("completed reasoning response references an unprovisioned source coordinate output=%d part=%d", coordinate.output, coordinate.part)
			}
			if entry.ItemIdentity != nil {
				if err := entry.ItemIdentity.Validate(); err != nil {
					return err
				}
				alias, err := llm.ReasoningItemIdentityAlias(*entry.ItemIdentity)
				if err != nil {
					return err
				}
				if existing, ok := seenAliases[alias]; ok && existing != coordinate {
					return fmt.Errorf("completed reasoning response aliases one provider item to multiple source coordinates")
				}
				seenAliases[alias] = coordinate
				if err := s.engine.transcriptRuntimeState().ObserveReasoningItemIdentity(stepID, entry.SourceCoordinate, entry.ItemIdentity); err != nil {
					return err
				}
			}
			identity := cloneTranscriptReasoningTraceIdentity(&trace.Identity)
			receipt, err := s.engine.steerWithCommitReceipt(
				stepID,
				steerReasoningLocalEntryIntent(storedLocalEntry{
					Visibility: transcript.EntryVisibilityDetail,
					Role:       string(transcript.EntryRoleReasoning),
					Text:       entry.Text,
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
		receipt, err := s.engine.steerWithCommitReceipt(
			stepID,
			steerLocalEntryIntent(storedLocalEntry{
				Visibility: transcript.EntryVisibilityDetail,
				Role:       string(transcript.EntryRoleReasoning),
				Text:       entry.Text,
			}),
		)
		if err != nil {
			return err
		}
		if !receipt.Committed {
			return fmt.Errorf("completed-only reasoning persistence returned without a durable commit")
		}
	}
	_, remaining := s.engine.transcriptRuntimeState().ReasoningSnapshot()
	if len(remaining) != 0 {
		return fmt.Errorf("accepted response left provisional reasoning traces unresolved")
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
