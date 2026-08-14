package runtime

import (
	"errors"

	"core/server/llm"
	"core/server/session"
	"core/shared/invariant"
)

// SteerPersistedMessage applies a model-visible message to a dormant Session
// through the same steering owner used by a live Engine. The caller must hold
// the Session's dormant admission for the duration of the call.
func SteerPersistedMessage(
	store *session.Store,
	stepID string,
	message llm.Message,
) (session.CommitReceipt, error) {
	engine, err := newPersistedSteeringEngine(store)
	if err != nil {
		return session.CommitReceipt{}, err
	}
	return engine.steerWithCommitReceipt(
		stepID,
		steerMessagesWithPersistenceIntent(steeringMessageEventDefault,
			true,
			[]llm.Message{message},
		),
	)
}

func newPersistedSteeringEngine(store *session.Store) (*Engine, error) {
	if store == nil {
		return nil, errors.New("session store is required")
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		return nil, err
	}
	return &Engine{
		store:              store,
		eventLog:           eventLog,
		steering:           newSteeringQueue(),
		invariantPolicy:    invariant.OperationalPolicy(false),
		workflowControl:    newWorkflowControlState(),
		transcriptState:    newTranscriptRuntimeState(transcriptWorkingDir("", store.Meta().WorkspaceRoot)),
		modelRequestsState: newModelRequestRuntimeState(),
		stepLifecycle:      &defaultExclusiveStepLifecycle{},
	}, nil
}
