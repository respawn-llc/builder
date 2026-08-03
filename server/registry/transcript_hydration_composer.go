package registry

import (
	"context"
	"fmt"

	"core/server/runtime"
	"core/server/runtimeview"
	"core/shared/clientui"
)

func (r *RuntimeRegistry) composeTranscriptHydration(
	ctx context.Context,
	sessionID string,
	entry *authorityRuntimeEntry,
	snapshot runtime.TranscriptHydrationSnapshot,
) (clientui.TranscriptHydration, error) {
	hydration := runtimeview.TranscriptHydrationFromSnapshot(snapshot)
	readModel, err := r.runtimeReadModelFeedSnapshot(ctx, sessionID, nil)
	if err != nil {
		return clientui.TranscriptHydration{}, fmt.Errorf("build transcript runtime read model: %w", err)
	}
	hydration.RuntimeReadModelUpdate = readModel
	hydration.ActiveStep = transcriptActiveStepFromRuntimeReadModel(readModel)
	clearMismatchedActiveFacts(&hydration)
	hydration.SessionStatus, err = runtimeview.TranscriptSessionStatusFromRuntime(entry.engine)
	if err != nil {
		return clientui.TranscriptHydration{}, fmt.Errorf("build transcript session status: %w", err)
	}
	hydration.SessionIdentity, err = runtimeview.TranscriptSessionIdentityFromRuntime(entry.engine)
	if err != nil {
		return clientui.TranscriptHydration{}, fmt.Errorf("build transcript session identity: %w", err)
	}
	target, err := r.resolveSessionExecutionTarget(ctx, sessionID)
	if err != nil {
		return clientui.TranscriptHydration{}, err
	}
	hydration.SessionIdentity.ExecutionTarget = target

	hydration.PendingPrompts = r.transcriptPendingPrompts(sessionID, readModel.Activity.ActiveStep)
	hydration.BackgroundActivities, err = r.backgroundActivitiesForSession(sessionID)
	if err != nil {
		return clientui.TranscriptHydration{}, fmt.Errorf("build transcript background activities: %w", err)
	}
	if err := hydration.Validate(); err != nil {
		return clientui.TranscriptHydration{}, fmt.Errorf("validate canonical transcript hydration: %w", err)
	}
	return hydration, nil
}

func transcriptActiveStepFromRuntimeReadModel(
	update clientui.RuntimeReadModelUpdate,
) *clientui.TranscriptStepState {
	active := update.Activity.ActiveStep
	if active == nil {
		return nil
	}
	return &clientui.TranscriptStepState{
		RunID:      active.RunID,
		StepID:     active.StepID,
		Lifecycle:  clientui.StepLifecycleStarted,
		ActiveKind: active.ActiveKind,
		Status:     clientui.RunStatusRunning,
	}
}

func clearMismatchedActiveFacts(hydration *clientui.TranscriptHydration) {
	if hydration == nil {
		return
	}
	active := hydration.RuntimeReadModelUpdate.Activity.ActiveStep
	if active == nil {
		hydration.ActiveAssistant = nil
		hydration.ActiveReasoning = nil
		hydration.ActiveStep = nil
		hydration.ActiveReviewer = nil
		hydration.ActiveCompaction = nil
		hydration.InFlightTools = nil
		return
	}
	if hydration.ActiveAssistant != nil && hydration.ActiveAssistant.StepID != active.StepID {
		hydration.ActiveAssistant = nil
	}
	if hydration.ActiveReasoning != nil && hydration.ActiveReasoning.StepID != active.StepID {
		hydration.ActiveReasoning = nil
	}
	if hydration.ActiveStep != nil &&
		(hydration.ActiveStep.RunID != active.RunID ||
			hydration.ActiveStep.StepID != active.StepID ||
			hydration.ActiveStep.ActiveKind != active.ActiveKind) {
		hydration.ActiveStep = nil
	}
	if hydration.ActiveReviewer != nil && hydration.ActiveReviewer.StepID != active.StepID {
		hydration.ActiveReviewer = nil
	}
	if hydration.ActiveCompaction != nil && hydration.ActiveCompaction.StepID != active.StepID {
		hydration.ActiveCompaction = nil
	}
	if len(hydration.InFlightTools) > 0 {
		tools := hydration.InFlightTools[:0]
		for _, tool := range hydration.InFlightTools {
			if tool.StepID == active.StepID {
				tools = append(tools, tool)
			}
		}
		hydration.InFlightTools = tools
	}
}

func (r *RuntimeRegistry) transcriptPendingPrompts(
	sessionID string,
	activeStep *clientui.RuntimeActiveStep,
) []clientui.TranscriptPrompt {
	snapshots := r.pendingPrompts.List(sessionID)
	if len(snapshots) == 0 {
		return nil
	}
	prompts := make([]clientui.TranscriptPrompt, 0, len(snapshots))
	for _, snapshot := range snapshots {
		prompt := transcriptPendingPromptFromSnapshot(sessionID, snapshot, pendingPromptEventPending)
		if activeStep == nil || prompt.StepID != activeStep.StepID {
			continue
		}
		prompts = append(prompts, prompt)
	}
	return prompts
}
