package sessionservice

import (
	"context"
	"errors"
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
		out = append(out, session.InputDraftRecoveryBuffer{
			Kind:                     string(buffer.Kind),
			ID:                       strings.TrimSpace(buffer.ID),
			ServerID:                 strings.TrimSpace(buffer.ServerID),
			ClientRequestID:          strings.TrimSpace(buffer.ClientRequestID),
			Text:                     buffer.Text,
			OperationClientRequestID: strings.TrimSpace(buffer.OperationRef.ClientRequestID),
			OperationQueueItemID:     strings.TrimSpace(buffer.OperationRef.QueueItemID),
			OperationKind:            string(buffer.OperationRef.Kind),
		})
	}
	return out
}

func sessionRecoveryBuffersToAPI(buffers []session.InputDraftRecoveryBuffer) []serverapi.SessionDraftRecoveryBuffer {
	if len(buffers) == 0 {
		return nil
	}
	out := make([]serverapi.SessionDraftRecoveryBuffer, 0, len(buffers))
	for _, buffer := range buffers {
		out = append(out, serverapi.SessionDraftRecoveryBuffer{
			Kind:            serverapi.SessionDraftRecoveryBufferKind(strings.TrimSpace(buffer.Kind)),
			ID:              strings.TrimSpace(buffer.ID),
			ServerID:        strings.TrimSpace(buffer.ServerID),
			ClientRequestID: strings.TrimSpace(buffer.ClientRequestID),
			Text:            buffer.Text,
			OperationRef: clientui.RuntimeOperationRef{
				Kind:            clientui.RuntimeOperationKind(strings.TrimSpace(buffer.OperationKind)),
				ClientRequestID: strings.TrimSpace(buffer.OperationClientRequestID),
				QueueItemID:     strings.TrimSpace(buffer.OperationQueueItemID),
			},
		})
	}
	return out
}

func resolveSessionTransition(_ context.Context, req sessionTransitionResolveRequest) (serverapi.SessionLifecycleResult, error) {
	switch req.Transition.Action {
	case serverapi.SessionTransitionActionNewSession:
		parentID, err := optionalSessionID(req.Transition.ParentSessionID)
		if err != nil {
			return serverapi.SessionLifecycleResult{}, err
		}
		prompt, err := sessionInitialPromptMetadata(req.Transition.InitialPrompt, req.Transition.InitialPromptHistoryRecorded)
		if err != nil {
			return serverapi.SessionLifecycleResult{}, err
		}
		return serverapi.LaunchSessionLifecycleResult(
			serverapi.CreateNewSessionLaunchIntent(parentID),
			serverapi.NewSessionLaunchPreparation(
				prompt,
				serverapi.RestoreStoredDraftSessionInitialInputPolicy(),
				serverapi.SessionAuthPreparationKeepCurrent,
			),
		), nil
	case serverapi.SessionTransitionActionResume:
		return serverapi.SelectSessionLifecycleResult(serverapi.SessionAuthPreparationKeepCurrent), nil
	case serverapi.SessionTransitionActionOpenSession:
		targetID, err := runtimeids.ParseSessionID(strings.TrimSpace(req.Transition.TargetSessionID))
		if err != nil {
			return serverapi.SessionLifecycleResult{}, err
		}
		return serverapi.LaunchSessionLifecycleResult(
			serverapi.OpenExistingSessionLaunchIntent(targetID),
			serverapi.NewSessionLaunchPreparation(
				nil,
				serverapi.OverrideStoredDraftSessionInitialInputPolicy(req.Transition.InitialInput),
				serverapi.SessionAuthPreparationKeepCurrent,
			),
		), nil
	case serverapi.SessionTransitionActionForkRollback:
		return resolveForkRollback(req)
	default:
		return serverapi.StopSessionLifecycleResult(), nil
	}
}

func resolveForkRollback(req sessionTransitionResolveRequest) (serverapi.SessionLifecycleResult, error) {
	if req.Store == nil {
		return serverapi.SessionLifecycleResult{}, errors.New("current store is required for rollback fork")
	}
	if req.Transition.ForkUserMessageSeq <= 0 {
		return serverapi.SessionLifecycleResult{}, errors.New("rollback fork user message seq must be > 0")
	}
	parentMeta := req.Store.Meta()
	baseName := strings.TrimSpace(parentMeta.Name)
	if baseName == "" {
		baseName = parentMeta.SessionID
	}
	forkedStore, forkOrdinal, err := session.ForkAtUserMessage(req.Store, req.Transition.ForkUserMessageSeq, baseName, sessioncontract.SessionCategoryMain)
	if err != nil {
		return serverapi.SessionLifecycleResult{}, err
	}
	if err := forkedStore.SetName(strings.TrimSpace(baseName + " \u2192 edit u" + strconv.Itoa(forkOrdinal))); err != nil {
		return serverapi.SessionLifecycleResult{}, errors.Join(err, forkedStore.RemoveDurable())
	}
	forkID, err := runtimeids.ParseSessionID(forkedStore.Meta().SessionID)
	if err != nil {
		return serverapi.SessionLifecycleResult{}, errors.Join(err, forkedStore.RemoveDurable())
	}
	prompt, err := sessionInitialPromptMetadata(req.Transition.InitialPrompt, req.Transition.InitialPromptHistoryRecorded)
	if err != nil {
		return serverapi.SessionLifecycleResult{}, errors.Join(err, forkedStore.RemoveDurable())
	}
	return serverapi.LaunchSessionLifecycleResult(
		serverapi.OpenExistingSessionLaunchIntent(forkID),
		serverapi.NewSessionLaunchPreparation(
			prompt,
			serverapi.RestoreStoredDraftSessionInitialInputPolicy(),
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
