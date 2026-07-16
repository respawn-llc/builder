package sessionservice

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"core/server/session"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

type sessionTransition struct {
	Action                       serverapi.SessionTransitionAction
	InitialPrompt                string
	InitialPromptHistoryRecorded bool
	InitialInput                 string
	TargetSessionID              string
	ForkUserMessageSeq           int64
	ParentSessionID              string
}

type sessionTransitionResolveRequest struct {
	Store      *session.Store
	Transition sessionTransition
}

func initialSessionInput(store *session.Store, transitionInput string) string {
	if store == nil {
		return transitionInput
	}
	if draft := store.Meta().InputDraft; draft != "" {
		return draft
	}
	return transitionInput
}

func persistSessionInputDraft(store *session.Store, input string) error {
	return persistSessionInputDraftRecovery(store, input, nil)
}

func persistSessionInputDraftRecovery(store *session.Store, input string, buffers []serverapi.SessionDraftRecoveryBuffer) error {
	if store == nil {
		return nil
	}
	return store.SetInputDraftRecovery(input, sessionRecoveryBuffersFromAPI(buffers))
}

func sessionRecoveryBuffersFromAPI(buffers []serverapi.SessionDraftRecoveryBuffer) []session.InputDraftRecoveryBuffer {
	if len(buffers) == 0 {
		return nil
	}
	out := make([]session.InputDraftRecoveryBuffer, 0, len(buffers))
	for _, buffer := range buffers {
		operationQueueItemID := ""
		if buffer.OperationRef.QueueItemID != nil {
			operationQueueItemID = buffer.OperationRef.QueueItemID.String()
		}
		out = append(out, session.InputDraftRecoveryBuffer{
			Kind:                     string(buffer.Kind),
			ID:                       strings.TrimSpace(buffer.ID),
			ServerID:                 strings.TrimSpace(buffer.ServerID),
			ClientRequestID:          strings.TrimSpace(buffer.ClientRequestID),
			Text:                     buffer.Text,
			OperationClientRequestID: buffer.OperationRef.ClientRequestID.String(),
			OperationQueueItemID:     operationQueueItemID,
			OperationKind:            string(buffer.OperationRef.Kind),
		})
	}
	return out
}

func sessionRecoveryBuffersToAPI(buffers []session.InputDraftRecoveryBuffer) ([]serverapi.SessionDraftRecoveryBuffer, error) {
	if len(buffers) == 0 {
		return nil, nil
	}
	out := make([]serverapi.SessionDraftRecoveryBuffer, 0, len(buffers))
	for index, buffer := range buffers {
		operationRef, err := sessionRecoveryOperationRef(buffer)
		if err != nil {
			return nil, fmt.Errorf("restore session draft recovery operation %d: %w", index, err)
		}
		out = append(out, serverapi.SessionDraftRecoveryBuffer{
			Kind:            serverapi.SessionDraftRecoveryBufferKind(strings.TrimSpace(buffer.Kind)),
			ID:              strings.TrimSpace(buffer.ID),
			ServerID:        strings.TrimSpace(buffer.ServerID),
			ClientRequestID: strings.TrimSpace(buffer.ClientRequestID),
			Text:            buffer.Text,
			OperationRef:    operationRef,
		})
	}
	return out, nil
}

func sessionRecoveryOperationRef(buffer session.InputDraftRecoveryBuffer) (clientui.RuntimeOperationRef, error) {
	kind := clientui.RuntimeOperationKind(strings.TrimSpace(buffer.OperationKind))
	if kind == "" {
		return clientui.RuntimeOperationRef{}, nil
	}
	clientRequestID, err := runtimeids.ParseRuntimeClientRequestID(strings.TrimSpace(buffer.OperationClientRequestID))
	if err != nil {
		return clientui.RuntimeOperationRef{}, err
	}
	ref := clientui.RuntimeOperationRef{
		Kind:            kind,
		ClientRequestID: clientRequestID,
	}
	if rawQueueItemID := strings.TrimSpace(buffer.OperationQueueItemID); rawQueueItemID != "" {
		queueItemID, err := runtimeids.ParseQueueItemID(rawQueueItemID)
		if err != nil {
			return clientui.RuntimeOperationRef{}, err
		}
		ref.QueueItemID = &queueItemID
	}
	if err := ref.Validate(); err != nil {
		return clientui.RuntimeOperationRef{}, err
	}
	return ref, nil
}

func resolveSessionTransition(_ context.Context, req sessionTransitionResolveRequest) (serverapi.SessionDirective, error) {
	switch req.Transition.Action {
	case serverapi.SessionTransitionActionNewSession:
		parentID, err := optionalSessionID(req.Transition.ParentSessionID)
		if err != nil {
			return serverapi.SessionDirective{}, err
		}
		prompt, err := sessionInitialPromptMetadata(req.Transition.InitialPrompt, req.Transition.InitialPromptHistoryRecorded)
		if err != nil {
			return serverapi.SessionDirective{}, err
		}
		return serverapi.LaunchSessionDirective(
			serverapi.CreateNewSessionLaunchIntent(parentID),
			serverapi.NewSessionLaunchPreparation(
				prompt,
				serverapi.RestoreStoredDraftSessionDraftDisposition(),
				serverapi.SessionAuthPreparationKeepCurrent,
			),
		), nil
	case serverapi.SessionTransitionActionResume:
		return serverapi.SelectSessionDirective(serverapi.SessionAuthPreparationKeepCurrent), nil
	case serverapi.SessionTransitionActionOpenSession:
		targetID, err := runtimeids.ParseSessionID(strings.TrimSpace(req.Transition.TargetSessionID))
		if err != nil {
			return serverapi.SessionDirective{}, err
		}
		return serverapi.LaunchSessionDirective(
			serverapi.OpenExistingSessionLaunchIntent(targetID),
			serverapi.NewSessionLaunchPreparation(
				nil,
				serverapi.OverrideStoredDraftSessionDraftDisposition(req.Transition.InitialInput),
				serverapi.SessionAuthPreparationKeepCurrent,
			),
		), nil
	case serverapi.SessionTransitionActionForkRollback:
		return resolveForkRollback(req)
	default:
		return serverapi.StopSessionDirective(), nil
	}
}

func resolveForkRollback(req sessionTransitionResolveRequest) (serverapi.SessionDirective, error) {
	if req.Store == nil {
		return serverapi.SessionDirective{}, errors.New("current store is required for rollback fork")
	}
	if req.Transition.ForkUserMessageSeq <= 0 {
		return serverapi.SessionDirective{}, errors.New("rollback fork user message seq must be > 0")
	}
	parentMeta := req.Store.Meta()
	baseName := strings.TrimSpace(parentMeta.Name)
	if baseName == "" {
		baseName = parentMeta.SessionID
	}
	forkedStore, forkOrdinal, err := session.ForkAtUserMessage(req.Store, req.Transition.ForkUserMessageSeq, baseName, sessioncontract.SessionCategoryMain)
	if err != nil {
		return serverapi.SessionDirective{}, err
	}
	if err := forkedStore.SetName(strings.TrimSpace(baseName + " \u2192 edit u" + strconv.Itoa(forkOrdinal))); err != nil {
		return serverapi.SessionDirective{}, errors.Join(err, forkedStore.RemoveDurable())
	}
	forkID, err := runtimeids.ParseSessionID(forkedStore.Meta().SessionID)
	if err != nil {
		return serverapi.SessionDirective{}, errors.Join(err, forkedStore.RemoveDurable())
	}
	prompt, err := sessionInitialPromptMetadata(req.Transition.InitialPrompt, req.Transition.InitialPromptHistoryRecorded)
	if err != nil {
		return serverapi.SessionDirective{}, errors.Join(err, forkedStore.RemoveDurable())
	}
	return serverapi.LaunchSessionDirective(
		serverapi.OpenExistingSessionLaunchIntent(forkID),
		serverapi.NewSessionLaunchPreparation(
			prompt,
			serverapi.RestoreStoredDraftSessionDraftDisposition(),
			serverapi.SessionAuthPreparationKeepCurrent,
		),
	), nil
}

func optionalSessionID(raw string) (*runtimeids.SessionID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := runtimeids.ParseSessionID(trimmed)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func sessionInitialPromptMetadata(text string, historyRecorded bool) (*serverapi.SessionInitialPromptMetadata, error) {
	if strings.TrimSpace(text) == "" {
		if historyRecorded {
			return nil, errors.New("initial prompt history cannot be recorded without an initial prompt")
		}
		return nil, nil
	}
	prompt := serverapi.SessionInitialPromptMetadata{Text: text, HistoryRecorded: historyRecorded}
	if err := prompt.Validate(); err != nil {
		return nil, err
	}
	return &prompt, nil
}
