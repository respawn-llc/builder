package app

import (
	"fmt"
	"strings"
	"time"

	"core/shared/invariant"
	"core/shared/lifecyclecontract"
	"core/shared/runtimeids"
)

type clientHookAttachmentOpenFact struct {
	occurredAt     time.Time
	openingKind    lifecyclecontract.OpeningKind
	sessionID      runtimeids.SessionID
	sessionTitle   *string
	workflowTaskID *lifecyclecontract.WorkflowTaskID
}

func (f clientHookAttachmentOpenFact) validate() error {
	if f.occurredAt.IsZero() {
		return fmt.Errorf("client hook attachment open occurrence time is required")
	}
	switch f.openingKind {
	case lifecyclecontract.OpeningKindNew, lifecyclecontract.OpeningKindResumed:
	default:
		return fmt.Errorf("client hook attachment opening kind is invalid")
	}
	if f.sessionID.IsZero() {
		return fmt.Errorf("client hook attachment session id is required")
	}
	if _, err := runtimeids.ParseSessionID(f.sessionID.String()); err != nil {
		return err
	}
	if f.sessionTitle != nil && strings.TrimSpace(*f.sessionTitle) == "" {
		return fmt.Errorf("client hook attachment session title cannot be blank")
	}
	if f.workflowTaskID != nil {
		return f.workflowTaskID.Validate()
	}
	return nil
}

func (f clientHookAttachmentOpenFact) clone() clientHookAttachmentOpenFact {
	copied := f
	if f.sessionTitle != nil {
		title := strings.Clone(*f.sessionTitle)
		copied.sessionTitle = &title
	}
	if f.workflowTaskID != nil {
		taskID := *f.workflowTaskID
		copied.workflowTaskID = &taskID
	}
	return copied
}

func openClientHookAttachment(
	runtimePlan *runtimeLaunchPlan,
	attachmentPlan *clientHookAttachmentPlan,
	debug bool,
) error {
	if attachmentPlan == nil {
		return nil
	}
	if runtimePlan == nil || runtimePlan.Wiring == nil {
		return fmt.Errorf("client hook attachment requires prepared runtime wiring")
	}
	if runtimePlan.lifecycleHookDispatcher != nil {
		return nil
	}
	wiring := runtimePlan.Wiring
	if wiring.eventDispatcher == nil {
		return fmt.Errorf("client hook attachment requires accepted event dispatcher")
	}
	mode := invariant.ModeDiagnostic
	if debug {
		mode = invariant.ModePanic
	}
	dispatcher, err := newLifecycleHookDispatcher(
		attachmentPlan.Argv(),
		lifecyclecontract.NewEncoder(invariant.NewPolicy(invariant.WithMode(mode))),
	)
	if err != nil {
		return err
	}
	focus := func() bool { return false }
	if wiring.terminalFocus != nil {
		focus = wiring.terminalFocus.FocusedForAttention
	}
	coordinator := newClientLifecycleCoordinator(
		dispatcher,
		attachmentPlan.lifecycleContext,
		focus,
		nil,
	)
	openFact := clientHookAttachmentOpenFact{
		occurredAt:     time.Now().UTC(),
		openingKind:    attachmentPlan.openingKind,
		sessionID:      attachmentPlan.sessionID,
		sessionTitle:   attachmentPlan.SessionTitle(),
		workflowTaskID: attachmentPlan.WorkflowTaskID(),
	}
	if err := openFact.validate(); err != nil {
		_ = dispatcher.Close()
		return err
	}
	if !wiring.eventDispatcher.OpenClientHookAttachment(openFact) {
		_ = dispatcher.Close()
		return fmt.Errorf("client hook attachment open was already installed")
	}
	runtimePlan.lifecycleHookDispatcher = dispatcher
	wiring.lifecycleCoordinator = coordinator
	wiring.eventDispatcher.lifecycleHookIssues = dispatcher.Issues()
	return nil
}
