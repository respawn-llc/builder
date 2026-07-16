package runtimeids

type RuntimeClientRequestID struct{ uuidv4Value }

func ParseRuntimeClientRequestID(raw string) (RuntimeClientRequestID, error) {
	id, err := parseUUIDv4Value(raw, "client_request_id")
	return RuntimeClientRequestID{uuidv4Value: id}, err
}

func NewRuntimeClientRequestID() RuntimeClientRequestID {
	return RuntimeClientRequestID{uuidv4Value: newUUIDv4Value()}
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

func ParseLiveRunGroupID(raw string) (LiveRunGroupID, error) {
	id, err := parseUUIDv4Value(raw, "live_run_group_id")
	return LiveRunGroupID{uuidv4Value: id}, err
}

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
