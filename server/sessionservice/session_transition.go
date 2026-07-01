package sessionservice

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"core/server/session"
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
	if draft := store.Meta().InputDraft; draft != "" {
		return draft
	}
	return transitionInput
}

func persistSessionInputDraft(store *session.Store, input string) error {
	if store == nil {
		return nil
	}
	return store.SetInputDraft(input)
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
