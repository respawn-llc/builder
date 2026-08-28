package runtimeids

import "fmt"

type RuntimeClientRequestID struct{ uuidv4Value }

func NewRuntimeClientRequestID() RuntimeClientRequestID {
	return RuntimeClientRequestID{uuidv4Value: newUUIDv4Value()}
}

type ExecutionScopeID struct{ uuidv4Value }

func NewExecutionScopeID() ExecutionScopeID {
	return ExecutionScopeID{uuidv4Value: newUUIDv4Value()}
}

type ReasoningTraceID struct{ uuidv4Value }

func ParseReasoningTraceID(raw string) (ReasoningTraceID, error) {
	id, err := parseUUIDv4Value(raw, "reasoning_trace_id")
	return ReasoningTraceID{uuidv4Value: id}, err
}

func NewReasoningTraceID() ReasoningTraceID {
	return ReasoningTraceID{uuidv4Value: newUUIDv4Value()}
}

type ResourceGeneration uint64

func (g ResourceGeneration) Validate() error {
	if g == 0 {
		return fmt.Errorf("runtime resource generation must be positive")
	}
	return nil
}

type SessionResourceRef struct {
	sessionID  SessionID
	generation ResourceGeneration
}

func NewSessionResourceRef(sessionID SessionID, generation ResourceGeneration) (SessionResourceRef, error) {
	if sessionID.IsZero() {
		return SessionResourceRef{}, fmt.Errorf("session resource session_id is required")
	}
	if err := generation.Validate(); err != nil {
		return SessionResourceRef{}, err
	}
	return SessionResourceRef{sessionID: sessionID, generation: generation}, nil
}

func (r SessionResourceRef) SessionID() SessionID {
	return r.sessionID
}

func (r SessionResourceRef) Generation() ResourceGeneration {
	return r.generation
}

func (r SessionResourceRef) Validate() error {
	if r.sessionID.IsZero() {
		return fmt.Errorf("session resource session_id is required")
	}
	return r.generation.Validate()
}

type ExecutionCorrelation struct {
	scopeID            ExecutionScopeID
	resourceGeneration ResourceGeneration
}

func NewExecutionCorrelation(scopeID ExecutionScopeID, resourceGeneration ResourceGeneration) (ExecutionCorrelation, error) {
	if scopeID.IsZero() {
		return ExecutionCorrelation{}, fmt.Errorf("execution scope id is required")
	}
	if err := resourceGeneration.Validate(); err != nil {
		return ExecutionCorrelation{}, err
	}
	return ExecutionCorrelation{scopeID: scopeID, resourceGeneration: resourceGeneration}, nil
}

func (c ExecutionCorrelation) ScopeID() ExecutionScopeID {
	return c.scopeID
}

func (c ExecutionCorrelation) ResourceGeneration() ResourceGeneration {
	return c.resourceGeneration
}

func (c ExecutionCorrelation) Validate() error {
	if c.scopeID.IsZero() {
		return fmt.Errorf("execution scope id is required")
	}
	return c.resourceGeneration.Validate()
}

type QueueItemID struct{ uuidv4Value }

func ParseQueueItemID(raw string) (QueueItemID, error) {
	id, err := parseUUIDv4Value(raw, "queue_item_id")
	return QueueItemID{uuidv4Value: id}, err
}

func NewQueueItemID() QueueItemID {
	return QueueItemID{uuidv4Value: newUUIDv4Value()}
}

type LiveRunGroupID struct{ uuidv4Value }

func NewLiveRunGroupID() LiveRunGroupID {
	return LiveRunGroupID{uuidv4Value: newUUIDv4Value()}
}

type RunID struct{ uuidv4Value }

func ParseRunID(raw string) (RunID, error) {
	id, err := parseUUIDv4Value(raw, "run_id")
	return RunID{uuidv4Value: id}, err
}

type StepID struct{ uuidv4Value }

func ParseStepID(raw string) (StepID, error) {
	id, err := parseUUIDv4Value(raw, "step_id")
	return StepID{uuidv4Value: id}, err
}

type CompactionRequestID struct{ uuidv4Value }

func NewCompactionRequestID() CompactionRequestID {
	return CompactionRequestID{uuidv4Value: newUUIDv4Value()}
}

func ParseCompactionRequestID(raw string) (CompactionRequestID, error) {
	id, err := parseUUIDv4Value(raw, "compaction_request_id")
	return CompactionRequestID{uuidv4Value: id}, err
}

type AssistantStreamID struct{ uuidv4Value }

func ParseAssistantStreamID(raw string) (AssistantStreamID, error) {
	id, err := parseUUIDv4Value(raw, "assistant_stream_id")
	return AssistantStreamID{uuidv4Value: id}, err
}

func NewAssistantStreamID() AssistantStreamID {
	return AssistantStreamID{uuidv4Value: newUUIDv4Value()}
}

type BackgroundActivityID struct{ uuidv4Value }

func ParseBackgroundActivityID(raw string) (BackgroundActivityID, error) {
	id, err := parseUUIDv4Value(raw, "background_activity_id")
	return BackgroundActivityID{uuidv4Value: id}, err
}

func NewBackgroundActivityID() BackgroundActivityID {
	return BackgroundActivityID{uuidv4Value: newUUIDv4Value()}
}
