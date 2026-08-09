package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/toolspec"

	"github.com/google/uuid"
)

type defaultBackgroundAgendaAdapter struct {
	engine *Engine
}

type backgroundNoticeAgendaItem struct {
	id        boundaryAgendaItemID
	sessionID string
	message   llm.Message
	order     uint64
	settled   bool
}

type backgroundNoticeSelection struct {
	id      boundaryAgendaItemID
	message llm.Message
	item    *backgroundNoticeAgendaItem
}

func newBackgroundNoticeAgendaItem(msg llm.Message) (*backgroundNoticeAgendaItem, error) {
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		return nil, errors.New("background notice content is required")
	}
	sessionID, _ := textutil.OptionalTrimmed(msg.Name)
	activityID, hasActivityID := textutil.OptionalTrimmed(msg.BackgroundActivityID)
	id := boundaryAgendaItemID("technical-notice:" + uuid.NewString())
	if hasActivityID {
		id = boundaryAgendaItemID("background-notice:" + activityID)
	}
	return &backgroundNoticeAgendaItem{
		id:        id,
		sessionID: sessionID,
		message:   msg,
	}, nil
}

func (i *backgroundNoticeAgendaItem) agendaID() boundaryAgendaItemID {
	return i.id
}

func (*backgroundNoticeAgendaItem) agendaBinding() boundaryAgendaBinding {
	return runtimeBoundaryBinding()
}

func (*backgroundNoticeAgendaItem) agendaEligibility() boundaryEligibility {
	return boundaryEligibilitySafe
}

func (i *backgroundNoticeAgendaItem) agendaOrder() uint64 {
	return i.order
}

func (i *backgroundNoticeAgendaItem) setAgendaOrder(order uint64) {
	i.order = order
}

func (i *backgroundNoticeAgendaItem) settleBoundaryAgenda(err error) {
	if i.settled {
		panic(fmt.Sprintf("background Boundary Agenda item %q settled twice", i.id))
	}
	i.settled = true
}

func (i *backgroundNoticeAgendaItem) selectLongWork() boundaryLongWork {
	return &backgroundNoticeSelection{
		id:      i.id,
		message: i.message,
		item:    i,
	}
}

func (s *backgroundNoticeSelection) longWorkID() boundaryAgendaItemID {
	return s.id
}

func (s *backgroundNoticeSelection) runLongWork(ctx context.Context, engine *Engine) error {
	return engine.runBackgroundNoticeSelection(ctx, s)
}

func (s *backgroundNoticeSelection) settleLongWork(err error) {
	s.item.settleBoundaryAgenda(err)
}

func (s *backgroundNoticeSelection) completeRuntimeBoundLongWork(
	engine *Engine,
	err error,
) {
	engine.submitBoundaryLongWorkResult(s.id, err)
}

func (e *Engine) HandleBackgroundShellUpdate(evt BackgroundShellEvent, queueNotice bool) {
	e.ensureOrchestrationCollaborators()
	if err := e.backgroundFlow.HandleBackgroundShellUpdate(evt, queueNotice); err != nil {
		e.surfaceRunError(err)
	}
}

func (b *defaultBackgroundAgendaAdapter) HandleBackgroundShellUpdate(
	evt BackgroundShellEvent,
	queueNotice bool,
) error {
	var item *backgroundNoticeAgendaItem
	if queueNotice && evt.Type.IsTerminal() {
		var err error
		item, err = newBackgroundNoticeAgendaItem(backgroundShellDeveloperNotice(evt))
		if err != nil {
			return err
		}
	}
	_, err := submitRuntimeEvent(
		b.engine,
		struct {
			event BackgroundShellEvent
			item  *backgroundNoticeAgendaItem
		}{event: evt, item: item},
		func(
			admission runtimeEventAdmission,
			input struct {
				event BackgroundShellEvent
				item  *backgroundNoticeAgendaItem
			},
		) (struct{}, error) {
			if err := admission.applySteering("", steerEventIntent(Event{
				Kind:       EventBackgroundUpdated,
				Background: &input.event,
			})); err != nil {
				return struct{}{}, err
			}
			if input.item == nil {
				return struct{}{}, nil
			}
			return struct{}{}, b.acceptNotice(admission, input.item)
		},
	)
	return err
}

func (b *defaultBackgroundAgendaAdapter) QueueBackgroundShellContinuation(
	evt BackgroundShellEvent,
) error {
	if !evt.Type.IsTerminal() {
		return nil
	}
	return b.QueueDeveloperNotice(backgroundShellDeveloperNotice(evt))
}

func (b *defaultBackgroundAgendaAdapter) QueueDeveloperNotice(msg llm.Message) error {
	item, err := newBackgroundNoticeAgendaItem(msg)
	if err != nil {
		return err
	}
	_, err = submitRuntimeEvent(
		b.engine,
		item,
		func(
			admission runtimeEventAdmission,
			accepted *backgroundNoticeAgendaItem,
		) (struct{}, error) {
			return struct{}{}, b.acceptNotice(admission, accepted)
		},
	)
	return err
}

func (b *defaultBackgroundAgendaAdapter) acceptNotice(
	admission runtimeEventAdmission,
	item *backgroundNoticeAgendaItem,
) error {
	if err := b.engine.boundaryAgenda.accept(item); err != nil {
		return err
	}
	if b.engine.idleBoundaryReductionEligible() {
		return b.engine.reduceIdleBoundary(admission)
	}
	return nil
}

func (e *Engine) idleBoundaryReductionEligible() bool {
	return e.agentSteps.current == nil &&
		e.agentSteps.boundary == nil &&
		e.agentSteps.reducerGrant == nil &&
		e.longBoundary.selected == nil
}

func (e *Engine) startNextBackgroundLongWork(admission runtimeEventAdmission) error {
	if !e.idleBoundaryReductionEligible() || e.runtimeEvents == nil {
		return nil
	}
	next, ok := e.boundaryAgenda.peekNext(idleBoundarySelection()).(*backgroundNoticeAgendaItem)
	if !ok {
		return nil
	}
	return e.startNextRuntimeBoundLongWork(admission, next.id)
}

func (e *Engine) runBackgroundNoticeSelection(
	ctx context.Context,
	selected *backgroundNoticeSelection,
) (assistantErr error) {
	return e.stepLifecycle.Run(
		ctx,
		exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindBackground},
		func(stepCtx context.Context, stepID string) error {
			if err := e.ensureMetaContextForRequest(stepCtx, stepID); err != nil {
				return err
			}
			origin, err := e.prepareBackgroundAgentStep(stepCtx)
			if err != nil {
				return fmt.Errorf("prepare background Agent Step: %w", err)
			}
			if err := e.applySelectedBackgroundNotice(
				stepCtx,
				origin.StepID,
				selected,
			); err != nil {
				return errors.Join(
					fmt.Errorf("apply selected background notice: %w", err),
					e.failAgentStepScope(err),
				)
			}
			_, runErr := e.runStepLoop(stepCtx, stepID)
			if runErr != nil {
				return fmt.Errorf("run background Agent loop: %w", runErr)
			}
			return nil
		},
	)
}

func (e *Engine) prepareBackgroundAgentStep(
	ctx context.Context,
) (serverapi.RuntimeStepOrigin, error) {
	snapshot := e.stepLifecycle.Snapshot()
	if snapshot == nil || snapshot.RunID == "" {
		return serverapi.RuntimeStepOrigin{}, ErrActiveStepInactive
	}
	decision, err := submitRuntimeEventWithContext(
		e.lifecycleCtx,
		ctx,
		e,
		snapshot.RunID,
		func(
			admission runtimeEventAdmission,
			runID string,
		) (continueAgentStepDecision, error) {
			return e.registerAgentProviderStep(admission, runID, false)
		},
	)
	return decision.Origin, err
}

func (e *Engine) applySelectedBackgroundNotice(
	ctx context.Context,
	stepID string,
	selected *backgroundNoticeSelection,
) error {
	_, err := submitRuntimeEventWithContext(
		e.lifecycleCtx,
		ctx,
		e,
		selected,
		func(
			admission runtimeEventAdmission,
			work *backgroundNoticeSelection,
		) (struct{}, error) {
			if e.longBoundary.selected == nil ||
				e.longBoundary.selected.longWorkID() != work.longWorkID() {
				return struct{}{}, errors.New("selected background work is no longer owned")
			}
			_, applyErr := applyBackgroundNoticeMessage(admission, stepID, work.message)
			return struct{}{}, applyErr
		},
	)
	return err
}

func (e *Engine) applyBackgroundNoticeBoundary(
	admission runtimeEventAdmission,
	stepID string,
	selection boundarySelection,
) (int, error) {
	selected := e.boundaryAgenda.selectNext(selection)
	item, ok := selected.(*backgroundNoticeAgendaItem)
	if !ok {
		if selected == nil {
			return 0, nil
		}
		return 0, fmt.Errorf("background reducer selected unexpected Boundary Agenda item %T", selected)
	}
	receipt, err := applyBackgroundNoticeMessage(admission, stepID, item.message)
	item.settleBoundaryAgenda(err)
	if receipt.Committed {
		return 1, err
	}
	return 0, err
}

func applyBackgroundNoticeMessage(
	admission runtimeEventAdmission,
	stepID string,
	message llm.Message,
) (session.CommitReceipt, error) {
	receipt := session.CommitReceipt{}
	intent := steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventDefault,
		true,
		[]llm.Message{message},
	)
	intent.items[0].commitReceipt = &receipt
	err := admission.applySteering(stepID, intent)
	if err == nil && !receipt.Committed {
		err = errors.New("background notice message was not committed")
	}
	return receipt, err
}

func backgroundShellDeveloperNotice(evt BackgroundShellEvent) llm.Message {
	return llm.Message{
		Role:                 llm.RoleDeveloper,
		MessageType:          textutil.Value(llm.MessageTypeBackgroundNotice),
		Name:                 textutil.OptionalTrimmedString(evt.ID),
		BackgroundActivityID: textutil.Value(evt.ActivityID.String()),
		Content:              textutil.Value(formatBackgroundShellNotice(evt)),
		CompactContent:       textutil.Value(formatBackgroundShellCompact(evt)),
		BackgroundExitCode:   textutil.Pointer(evt.ExitCode),
	}
}

func formatBackgroundShellNotice(evt BackgroundShellEvent) string {
	if strings.TrimSpace(evt.NoticeText) != "" {
		return strings.TrimSpace(evt.NoticeText)
	}
	parts := []string{fmt.Sprintf("Background shell %s %s.", evt.ID, evt.State)}
	if code := evt.ExitCode; code != nil {
		parts = append(parts, fmt.Sprintf("Exit code: %d", *code))
	}
	preview := strings.TrimSpace(evt.Preview)
	if preview != "" {
		parts = append(parts, "Output:")
		parts = append(parts, preview)
	} else {
		parts = append(parts, "No output")
	}
	return strings.Join(parts, "\n")
}

func formatBackgroundShellCompact(evt BackgroundShellEvent) string {
	if strings.TrimSpace(evt.CompactText) != "" {
		return strings.TrimSpace(evt.CompactText)
	}
	text := fmt.Sprintf("Background shell %s %s", evt.ID, evt.State)
	if code := evt.ExitCode; code != nil {
		text = fmt.Sprintf("%s (exit %d)", text, *code)
	}
	return text
}

type harvestedBackgroundCompletion struct {
	SessionID  int  `json:"background_session_id"`
	Running    bool `json:"background_running"`
	Background bool `json:"backgrounded"`
}

func harvestedBackgroundCompletionSessionID(res tools.Result) (string, bool) {
	if res.IsError || res.Name != toolspec.ToolWriteStdin {
		return "", false
	}
	var out harvestedBackgroundCompletion
	if err := json.Unmarshal(res.Output, &out); err != nil {
		return "", false
	}
	if out.SessionID <= 0 || out.Running || !out.Background {
		return "", false
	}
	return fmt.Sprintf("%d", out.SessionID), true
}
