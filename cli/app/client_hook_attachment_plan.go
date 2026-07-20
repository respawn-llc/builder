package app

import (
	"fmt"
	"slices"
	"strings"

	"core/shared/config"
	"core/shared/lifecyclecontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type clientHookAttachmentPlan struct {
	argv           []string
	openingKind    lifecyclecontract.OpeningKind
	sessionID      runtimeids.SessionID
	sessionTitle   *string
	workflowTaskID *lifecyclecontract.WorkflowTaskID
}

func deriveClientHookAttachmentPlan(
	settings config.ClientSettings,
	intent serverapi.SessionLaunchIntent,
	plan sessionLaunchPlan,
) (*clientHookAttachmentPlan, error) {
	command := settings.Hooks.LifecycleCommand()
	if len(command) == 0 || plan.Mode != launchModeInteractive {
		return nil, nil
	}
	if err := intent.Validate(); err != nil {
		return nil, fmt.Errorf("derive client hook attachment plan intent: %w", err)
	}
	var openingKind lifecyclecontract.OpeningKind
	switch intent.Kind() {
	case serverapi.SessionLaunchIntentCreateNew:
		openingKind = lifecyclecontract.OpeningKindNew
	case serverapi.SessionLaunchIntentOpenExisting:
		openingKind = lifecyclecontract.OpeningKindResumed
	default:
		return nil, fmt.Errorf("derive client hook attachment plan: unsupported opening intent %q", intent.Kind())
	}
	sessionID, err := runtimeids.ParseSessionID(plan.SessionID)
	if err != nil {
		return nil, fmt.Errorf("derive client hook attachment plan session id: %w", err)
	}
	captured := &clientHookAttachmentPlan{
		argv:        slices.Clone(command),
		openingKind: openingKind,
		sessionID:   sessionID,
	}
	if strings.TrimSpace(plan.SessionName) != "" {
		title := strings.Clone(plan.SessionName)
		captured.sessionTitle = &title
	}
	if plan.WorkflowTaskID != nil {
		taskID := *plan.WorkflowTaskID
		if err := taskID.Validate(); err != nil {
			return nil, fmt.Errorf("derive client hook attachment plan workflow task id: %w", err)
		}
		captured.workflowTaskID = &taskID
	}
	return captured, nil
}

func (p clientHookAttachmentPlan) Argv() []string {
	return slices.Clone(p.argv)
}

func (p clientHookAttachmentPlan) OpeningKind() lifecyclecontract.OpeningKind {
	return p.openingKind
}

func (p clientHookAttachmentPlan) SessionID() runtimeids.SessionID {
	return p.sessionID
}

func (p clientHookAttachmentPlan) SessionTitle() *string {
	if p.sessionTitle == nil {
		return nil
	}
	title := strings.Clone(*p.sessionTitle)
	return &title
}

func (p clientHookAttachmentPlan) WorkflowTaskID() *lifecyclecontract.WorkflowTaskID {
	if p.workflowTaskID == nil {
		return nil
	}
	taskID := *p.workflowTaskID
	return &taskID
}
