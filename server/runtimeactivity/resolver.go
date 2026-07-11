package runtimeactivity

import "core/shared/clientui"

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
	if !snapshot.Registry.Registered {
		return clientui.NewRuntimeActivity(clientui.RuntimeActivityUnavailable, clientui.RuntimeActivityOptions{})
	}
	if snapshot.Registry.Closing {
		return clientui.NewRuntimeActivity(clientui.RuntimeActivityClosing, clientui.RuntimeActivityOptions{})
	}
	if snapshot.Registry.Draining {
		return clientui.NewRuntimeActivity(clientui.RuntimeActivityDraining, clientui.RuntimeActivityOptions{})
	}
	if snapshot.Active != nil {
		state := clientui.RuntimeActivityRunning
		if snapshot.PromptWait {
			state = clientui.RuntimeActivityAwaitingPrompt
		}
		return clientui.NewRuntimeActivity(state, clientui.RuntimeActivityOptions{
			ActiveKind:     snapshot.Active.ActiveKind,
			RunID:          snapshot.Active.RunID,
			StepID:         snapshot.Active.StepID,
			QueueAccepting: snapshot.Registry.QueueAccepting,
		})
	}
	if snapshot.LiveRunActive {
		return clientui.NewRuntimeActivity(clientui.RuntimeActivityDraining, clientui.RuntimeActivityOptions{})
	}
	if snapshot.Registry.Starting || snapshot.PendingContinuation.Promoted {
		return clientui.NewRuntimeActivity(clientui.RuntimeActivityStarting, clientui.RuntimeActivityOptions{})
	}
	return clientui.NewRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{QueueAccepting: snapshot.Registry.QueueAccepting})
}
