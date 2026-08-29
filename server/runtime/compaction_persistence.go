package runtime

import (
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/shared/runtimeids"
	"core/shared/textutil"
)

type compactionPersistence struct {
	engine *Engine
}

func newCompactionPersistence(engine *Engine) compactionPersistence {
	return compactionPersistence{engine: engine}
}

func (p compactionPersistence) replaceHistory(stepID, engine string, mode compactionMode, items []llm.ResponseItem) (session.CommitReceipt, error) {
	e := p.engine
	return e.steerWithCommitReceipt(stepID, steerHistoryReplacementIntent(engine, mode, e.compactionRuntimeState().Count()+1, e.LastCommittedAssistantFinalAnswer(), items))
}

func (p compactionPersistence) setActivity(
	stepID string,
	requestID *runtimeids.CompactionRequestID,
	mode compactionMode,
	count int,
	activeKind ActiveKind,
	active bool,
) error {
	return p.engine.steer(stepID, steerCompactionActivityIntent(active, requestID, string(mode), count, activeKind))
}

func (p compactionPersistence) emitStatus(
	stepID string,
	requestID *runtimeids.CompactionRequestID,
	kind EventKind,
	mode compactionMode,
	engine, provider string,
	trimmed *int,
	count int,
	errText string,
) error {
	e := p.engine
	status := &CompactionStatus{
		Mode:              string(mode),
		RequestID:         requestID,
		Engine:            strings.TrimSpace(engine),
		Provider:          strings.TrimSpace(provider),
		TrimmedItemsCount: textutil.Pointer(trimmed),
		Count:             count,
		Error:             strings.TrimSpace(errText),
	}

	switch kind {
	case EventCompactionStarted:
		return e.steer(stepID, steerEventIntent(Event{
			Kind:       kind,
			StepID:     textutil.Value(stepID),
			Compaction: status,
		}))

	case EventCompactionCompleted:
		return e.steer(stepID, steerEventIntent(Event{
			Kind:       kind,
			StepID:     textutil.Value(stepID),
			Compaction: status,
		}))

	case EventCompactionFailed:
		message := fmt.Sprintf("Context compaction failed (%s): %s", status.Mode, status.Error)
		if strings.TrimSpace(status.Error) == "" {
			message = fmt.Sprintf("Context compaction failed (%s).", status.Mode)
		}
		if err := e.steer(stepID, steerLocalEntryIntent(storedLocalEntry{Role: "error", Text: message})); err != nil {
			_ = e.steer(stepID, steerEventIntent(Event{
				Kind:       kind,
				StepID:     textutil.Value(stepID),
				Compaction: status,
			}))

			return err
		}
		return e.steer(stepID, steerEventIntent(Event{
			Kind:       kind,
			StepID:     textutil.Value(stepID),
			Compaction: status,
		}))

	default:
		return nil
	}
}
