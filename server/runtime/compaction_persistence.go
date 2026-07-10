package runtime

import (
	"fmt"
	"strings"

	"core/server/llm"
	"core/shared/textutil"
)

type compactionPersistence struct {
	engine *Engine
}

func newCompactionPersistence(engine *Engine) compactionPersistence {
	return compactionPersistence{engine: engine}
}

func (p compactionPersistence) replaceHistory(stepID, engine string, mode compactionMode, items []llm.ResponseItem) error {
	e := p.engine
	workflowRunID := ""
	if e.cfg.WorkflowRun != nil {
		workflowRunID = strings.TrimSpace(string(e.cfg.WorkflowRun.RunID))
	}
	pendingHandoffFutureMessage := ""
	if req := e.handoffRuntimeState().RequestSnapshot(); req != nil {
		pendingHandoffFutureMessage = strings.TrimSpace(req.futureAgentMessage)
	}
	return e.steer(stepID, steerHistoryReplacementIntent(engine, mode, workflowRunID, e.compactionRuntimeState().Count()+1, pendingHandoffFutureMessage, e.LastCommittedAssistantFinalAnswer(), items))
}

func (p compactionPersistence) emitStatus(stepID string, kind EventKind, mode compactionMode, engine, provider string, trimmed *int, count int, errText string) error {
	e := p.engine
	status := &CompactionStatus{
		Mode:              string(mode),
		Engine:            strings.TrimSpace(engine),
		Provider:          strings.TrimSpace(provider),
		TrimmedItemsCount: textutil.CloneInt(trimmed),
		Count:             count,
		Error:             strings.TrimSpace(errText),
	}

	switch kind {
	case EventCompactionStarted:
		return e.steer(stepID, steerEventIntent(Event{
			Kind:       kind,
			StepID:     stepID,
			Compaction: status,
		}))

	case EventCompactionCompleted:
		return e.steer(stepID, steerEventIntent(Event{
			Kind:       kind,
			StepID:     stepID,
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
				StepID:     stepID,
				Compaction: status,
			}))

			return err
		}
		return e.steer(stepID, steerEventIntent(Event{
			Kind:       kind,
			StepID:     stepID,
			Compaction: status,
		}))

	default:
		return nil
	}
}
