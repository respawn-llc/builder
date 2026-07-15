package runtimeactivity

import (
	"fmt"

	"core/server/runtimefeed"
	"core/shared/clientui"
	"core/shared/runtimeids"
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
	LiveRunActive       bool
	PromptWait          bool
	PendingContinuation PendingContinuationSnapshot
}

func ResolveRuntimeActivity(snapshot ResolverSnapshot) (clientui.RuntimeActivity, error) {
	activity, err := resolveRuntimeFeedActivity(snapshot)
	if err != nil {
		return clientui.RuntimeActivity{}, err
	}
	return Protocol59RuntimeActivity(activity), nil
}

func resolveRuntimeFeedActivity(snapshot ResolverSnapshot) (runtimefeed.RuntimeActivity, error) {
	var activity runtimefeed.RuntimeActivity
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
			return runtimefeed.RuntimeActivity{}, fmt.Errorf("parse runtime active run id: %w", err)
		}
		stepID, err := runtimeids.ParseStepID(snapshot.Active.StepID)
		if err != nil {
			return runtimefeed.RuntimeActivity{}, fmt.Errorf("parse runtime active step id: %w", err)
		}
		activity.ActiveStep = &runtimefeed.RuntimeActiveStep{
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
		return runtimefeed.RuntimeActivity{}, err
	}
	return activity, nil
}
