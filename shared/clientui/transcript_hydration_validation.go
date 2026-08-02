package clientui

import (
	"fmt"

	"core/shared/runtimeids"
)

func (h TranscriptHydration) Validate() error {
	if err := h.SessionIdentity.Validate(); err != nil {
		return fmt.Errorf("validate transcript hydration session identity: %w", err)
	}
	if err := h.SessionStatus.Validate(); err != nil {
		return fmt.Errorf("validate transcript hydration session status: %w", err)
	}
	if err := h.RuntimeReadModelUpdate.Validate(); err != nil {
		return fmt.Errorf("validate transcript hydration runtime read-model update: %w", err)
	}
	if h.CommittedRows == nil {
		return fmt.Errorf("transcript hydration committed rows are required")
	}
	for index, row := range h.CommittedRows {
		if err := row.Validate(); err != nil {
			return fmt.Errorf("validate transcript hydration committed row %d: %w", index, err)
		}
	}
	if h.ActiveAssistant != nil {
		if err := h.ActiveAssistant.Validate(); err != nil {
			return fmt.Errorf("validate transcript hydration active assistant: %w", err)
		}
	}
	if h.ActiveReasoning != nil {
		if err := h.ActiveReasoning.Validate(); err != nil {
			return fmt.Errorf("validate transcript hydration active reasoning: %w", err)
		}
	}
	if h.ActiveStep != nil {
		if err := h.ActiveStep.Validate(); err != nil {
			return fmt.Errorf("validate transcript hydration active step: %w", err)
		}
		if h.ActiveStep.Lifecycle != StepLifecycleStarted {
			return fmt.Errorf("transcript hydration active step is not started")
		}
	}
	if h.ActiveReviewer != nil {
		if err := h.ActiveReviewer.Validate(); err != nil {
			return fmt.Errorf("validate transcript hydration active reviewer: %w", err)
		}
		if h.ActiveReviewer.State != ReviewerStateRunning {
			return fmt.Errorf("transcript hydration reviewer is not running")
		}
	}
	if h.ActiveCompaction != nil {
		if err := h.ActiveCompaction.Validate(); err != nil {
			return fmt.Errorf("validate transcript hydration active compaction: %w", err)
		}
		if h.ActiveCompaction.State != CompactionStarted {
			return fmt.Errorf("transcript hydration compaction is not active")
		}
	}
	if err := validateHydrationTools(h.InFlightTools); err != nil {
		return err
	}
	if err := validateHydrationQueuedMessages(h.QueuedMessages); err != nil {
		return err
	}
	if err := validateHydrationPrompts(h.PendingPrompts); err != nil {
		return err
	}
	if err := validateHydrationBackgrounds(h.BackgroundActivities); err != nil {
		return err
	}
	if err := h.validateActiveOwnership(); err != nil {
		return err
	}
	if h.ContextUsage != nil {
		if err := h.ContextUsage.Validate(); err != nil {
			return fmt.Errorf("validate transcript hydration context usage: %w", err)
		}
	}
	if h.GoalStatus != nil {
		if err := h.GoalStatus.Validate(); err != nil {
			return fmt.Errorf("validate transcript hydration goal status: %w", err)
		}
	}
	return nil
}

func (h TranscriptHydration) validateActiveOwnership() error {
	activeStep := h.RuntimeReadModelUpdate.Activity.ActiveStep
	if h.ActiveStep != nil {
		if activeStep == nil {
			return fmt.Errorf("transcript hydration active step state has no canonical runtime active step")
		}
		if h.ActiveStep.RunID != activeStep.RunID ||
			h.ActiveStep.StepID != activeStep.StepID ||
			h.ActiveStep.ActiveKind != activeStep.ActiveKind {
			return fmt.Errorf(
				"transcript hydration active step state (%s, %s, %s) does not match canonical runtime active step (%s, %s, %s)",
				h.ActiveStep.RunID.String(),
				h.ActiveStep.StepID.String(),
				h.ActiveStep.ActiveKind,
				activeStep.RunID.String(),
				activeStep.StepID.String(),
				activeStep.ActiveKind,
			)
		}
	}
	if h.ActiveAssistant != nil {
		if err := validateHydrationStepOwner("active assistant", h.ActiveAssistant.StepID, activeStep); err != nil {
			return err
		}
	}
	if h.ActiveReasoning != nil {
		if err := validateHydrationStepOwner("active reasoning", h.ActiveReasoning.StepID, activeStep); err != nil {
			return err
		}
	}
	if h.ActiveReviewer != nil {
		if err := validateHydrationStepOwner("active reviewer", h.ActiveReviewer.StepID, activeStep); err != nil {
			return err
		}
	}
	if h.ActiveCompaction != nil {
		if err := validateHydrationStepOwner("active compaction", h.ActiveCompaction.StepID, activeStep); err != nil {
			return err
		}
	}
	for index, tool := range h.InFlightTools {
		if err := validateHydrationStepOwner(fmt.Sprintf("in-flight tool %d", index), tool.StepID, activeStep); err != nil {
			return err
		}
	}
	for index, prompt := range h.PendingPrompts {
		if prompt.SessionID != h.SessionIdentity.SessionID {
			return fmt.Errorf(
				"transcript hydration pending prompt %d session id %q does not match hydrated session id %q",
				index,
				prompt.SessionID.String(),
				h.SessionIdentity.SessionID.String(),
			)
		}
		if err := validateHydrationStepOwner(fmt.Sprintf("pending prompt %d", index), prompt.StepID, activeStep); err != nil {
			return err
		}
	}
	return nil
}

func validateHydrationStepOwner(owner string, stepID runtimeids.StepID, activeStep *RuntimeActiveStep) error {
	if activeStep == nil {
		return fmt.Errorf("transcript hydration %s has no canonical runtime active step", owner)
	}
	if stepID != activeStep.StepID {
		return fmt.Errorf(
			"transcript hydration %s step id %q does not match canonical runtime active step id %q",
			owner,
			stepID.String(),
			activeStep.StepID.String(),
		)
	}
	return nil
}

func validateHydrationTools(tools []TranscriptToolStart) error {
	seen := make(map[ToolCallID]struct{}, len(tools))
	for index, tool := range tools {
		if err := tool.Validate(); err != nil {
			return fmt.Errorf("validate transcript hydration tool %d: %w", index, err)
		}
		if _, exists := seen[tool.ToolCallID]; exists {
			return fmt.Errorf("transcript hydration repeats tool call id %q", tool.ToolCallID)
		}
		seen[tool.ToolCallID] = struct{}{}
	}
	return nil
}

func validateHydrationQueuedMessages(messages []TranscriptQueuedMessageState) error {
	seenRequests := make(map[runtimeids.RuntimeClientRequestID]struct{}, len(messages))
	seenItems := make(map[runtimeids.QueueItemID]struct{}, len(messages))
	for index, message := range messages {
		if err := message.Validate(); err != nil {
			return fmt.Errorf("validate transcript hydration queued message %d: %w", index, err)
		}
		if message.Status != QueuedUserMessageAccepted {
			return fmt.Errorf("transcript hydration queued message %d is not accepted", index)
		}
		if _, exists := seenRequests[message.ClientRequestID]; exists {
			return fmt.Errorf("transcript hydration repeats queued client request id %q", message.ClientRequestID.String())
		}
		seenRequests[message.ClientRequestID] = struct{}{}
		if _, exists := seenItems[message.QueueItemID]; exists {
			return fmt.Errorf("transcript hydration repeats queue item id %q", message.QueueItemID.String())
		}
		seenItems[message.QueueItemID] = struct{}{}
	}
	return nil
}

func validateHydrationPrompts(prompts []TranscriptPrompt) error {
	seen := make(map[PromptID]struct{}, len(prompts))
	for index, prompt := range prompts {
		if err := prompt.Validate(); err != nil {
			return fmt.Errorf("validate transcript hydration prompt %d: %w", index, err)
		}
		if prompt.Status != TranscriptPromptStatusPending {
			return fmt.Errorf("transcript hydration prompt %d is not pending", index)
		}
		if _, exists := seen[prompt.PromptID]; exists {
			return fmt.Errorf("transcript hydration repeats prompt id %q", prompt.PromptID)
		}
		seen[prompt.PromptID] = struct{}{}
		if index == 0 {
			continue
		}
		previous := prompts[index-1]
		if previous.CreatedAt.After(prompt.CreatedAt) ||
			(previous.CreatedAt.Equal(prompt.CreatedAt) && string(previous.PromptID) > string(prompt.PromptID)) {
			return fmt.Errorf("transcript hydration prompts are not ordered by creation time then id")
		}
	}
	return nil
}

func validateHydrationBackgrounds(backgrounds []TranscriptBackgroundActivity) error {
	seen := make(map[runtimeids.BackgroundActivityID]struct{}, len(backgrounds))
	for index, background := range backgrounds {
		if err := background.Validate(); err != nil {
			return fmt.Errorf("validate transcript hydration background %d: %w", index, err)
		}
		if background.Lifecycle != BackgroundLifecycleBackgrounded {
			return fmt.Errorf("transcript hydration background %d is terminal", index)
		}
		if _, exists := seen[background.ActivityID]; exists {
			return fmt.Errorf("transcript hydration repeats background activity id %q", background.ActivityID.String())
		}
		seen[background.ActivityID] = struct{}{}
	}
	return nil
}
