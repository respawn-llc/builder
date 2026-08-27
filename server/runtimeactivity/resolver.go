package runtimeactivity

import (
	"core/shared/clientui"
	"core/shared/runtimeids"
	"fmt"
)

type RegistrySnapshot struct {
	Registered     bool
	QueueAccepting bool
	Draining       bool
	Closing        bool
	Starting       bool
}

type PendingContinuationSnapshot struct {
	Promoted bool
}

type ActiveStepSnapshot struct {
	RunID      string
	StepID     string
	ActiveKind clientui.RuntimeActivityActiveKind
}

type ResolverSnapshot struct {
	Registry            RegistrySnapshot
	Active              *ActiveStepSnapshot
	Reviewer            clientui.ReviewerActivity
	LiveRunActive       bool
	PromptWait          bool
	PendingContinuation PendingContinuationSnapshot
}

func ResolveRuntimeActivity(snapshot ResolverSnapshot) (clientui.RuntimeActivity, error) {
	return resolveRuntimeFeedActivity(snapshot)
}

func resolveRuntimeFeedActivity(snapshot ResolverSnapshot) (clientui.RuntimeActivity, error) {
	activity := clientui.RuntimeActivity{Reviewer: snapshot.Reviewer}
	if activity.Reviewer == "" {
		activity.Reviewer = clientui.ReviewerActivityInactive
	}
	if !snapshot.Registry.Registered {
		activity.State = clientui.RuntimeActivityUnavailable
	} else if snapshot.Registry.Closing {
		activity.State = clientui.RuntimeActivityClosing
	} else if snapshot.Registry.Draining {
		activity.State = clientui.RuntimeActivityDraining
	} else if snapshot.Active != nil {
		activity.State = clientui.RuntimeActivityRunning
		if snapshot.PromptWait {
			activity.State = clientui.RuntimeActivityAwaitingPrompt
		}
		runID, err := runtimeids.ParseRunID(snapshot.Active.RunID)
		if err != nil {
			return clientui.RuntimeActivity{}, fmt.Errorf("parse runtime active run id: %w", err)
		}
		stepID, err := runtimeids.ParseStepID(snapshot.Active.StepID)
		if err != nil {
			return clientui.RuntimeActivity{}, fmt.Errorf("parse runtime active step id: %w", err)
		}
		activity.ActiveStep = &clientui.RuntimeActiveStep{
			RunID:      runID,
			StepID:     stepID,
			ActiveKind: snapshot.Active.ActiveKind,
		}
		activity.QueueAccepting = snapshot.Registry.QueueAccepting
	} else if snapshot.LiveRunActive {
		activity.State = clientui.RuntimeActivityDraining
	} else if snapshot.Registry.Starting || snapshot.PendingContinuation.Promoted {
		activity.State = clientui.RuntimeActivityStarting
	} else {
		activity.State = clientui.RuntimeActivityRegisteredIdle
		activity.QueueAccepting = snapshot.Registry.QueueAccepting
	}
	if err := activity.Validate(); err != nil {
		return clientui.RuntimeActivity{}, err
	}
	return activity, nil
}
