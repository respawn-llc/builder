package sessionservice

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"core/server/session"
	"core/shared/clientui"
	"core/shared/serverapi"
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

type resolvedSessionTransition struct {
	NextSessionID                string
	InitialPrompt                string
	InitialPromptHistoryRecorded bool
	InitialInput                 string
	ParentSessionID              string
	ForceNewSession              bool
	ShouldContinue               bool
}

func initialSessionInput(store *session.Store, transitionInput string) string {
	if store == nil {
		return transitionInput
	}
	if transitionInput != "" {
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

func resolveSessionTransition(ctx context.Context, req sessionTransitionResolveRequest) (resolvedSessionTransition, error) {
	switch req.Transition.Action {
	case serverapi.SessionTransitionActionNewSession:
		return resolvedSessionTransition{
			InitialPrompt:                req.Transition.InitialPrompt,
			InitialPromptHistoryRecorded: req.Transition.InitialPromptHistoryRecorded,
			ParentSessionID:              req.Transition.ParentSessionID,
			ForceNewSession:              true,
			ShouldContinue:               true,
		}, nil
	case serverapi.SessionTransitionActionResume:
		return resolvedSessionTransition{ShouldContinue: true}, nil
	case serverapi.SessionTransitionActionOpenSession:
		return resolvedSessionTransition{
			NextSessionID:  strings.TrimSpace(req.Transition.TargetSessionID),
			InitialInput:   req.Transition.InitialInput,
			ShouldContinue: true,
		}, nil
	case serverapi.SessionTransitionActionForkRollback:
		return resolveForkRollback(req)
	default:
		return resolvedSessionTransition{}, nil
	}
}

func resolveForkRollback(req sessionTransitionResolveRequest) (resolvedSessionTransition, error) {
	if req.Store == nil {
		return resolvedSessionTransition{}, errors.New("current store is required for rollback fork")
	}
	if req.Transition.ForkUserMessageSeq <= 0 {
		return resolvedSessionTransition{}, errors.New("rollback fork user message seq must be > 0")
	}
	parentMeta := req.Store.Meta()
	baseName := strings.TrimSpace(parentMeta.Name)
	if baseName == "" {
		baseName = parentMeta.SessionID
	}
	forkedStore, forkOrdinal, err := session.ForkAtUserMessage(req.Store, req.Transition.ForkUserMessageSeq, baseName)
	if err != nil {
		return resolvedSessionTransition{}, err
	}
	if err := forkedStore.SetName(strings.TrimSpace(baseName + " \u2192 edit u" + strconv.Itoa(forkOrdinal))); err != nil {
		return resolvedSessionTransition{}, errors.Join(err, forkedStore.RemoveDurable())
	}
	return resolvedSessionTransition{
		NextSessionID:                forkedStore.Meta().SessionID,
		InitialPrompt:                req.Transition.InitialPrompt,
		InitialPromptHistoryRecorded: req.Transition.InitialPromptHistoryRecorded,
		ShouldContinue:               true,
	}, nil
}
