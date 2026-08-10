package runtime

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

var (
	errBoundaryRuntimeClosed  = errors.New("runtime Boundary Agenda is closed")
	errBoundaryScopeFinalized = errors.New("Exact Execution Scope is finalizing")
	errBoundaryScopeStopped   = errors.New("Exact Execution Scope was stopped")
)

type boundaryAgendaItemID string

type boundaryAgendaBinding interface {
	boundaryAgendaBinding()
}

type scopeAgendaBinding struct {
	scopeID runtimeids.ExecutionScopeID
	origin  serverapi.RuntimeStepOrigin
}

func scopeBoundaryBinding(
	scopeID runtimeids.ExecutionScopeID,
	origin serverapi.RuntimeStepOrigin,
) boundaryAgendaBinding {
	if scopeID.IsZero() {
		panic("scope-bound Boundary Agenda item requires an Exact Execution Scope")
	}
	if err := origin.Validate(); err != nil {
		panic(fmt.Sprintf("scope-bound Boundary Agenda item requires a valid Agent Step origin: %v", err))
	}
	return scopeAgendaBinding{
		scopeID: scopeID,
		origin:  origin,
	}
}

func (scopeAgendaBinding) boundaryAgendaBinding() {}

type continuationScopeAgendaBinding struct {
	scopeID runtimeids.ExecutionScopeID
}

func continuationScopeBoundaryBinding(
	scopeID runtimeids.ExecutionScopeID,
) boundaryAgendaBinding {
	if scopeID.IsZero() {
		panic("continuation scope Boundary Agenda item requires an Exact Execution Scope")
	}
	return continuationScopeAgendaBinding{scopeID: scopeID}
}

func (continuationScopeAgendaBinding) boundaryAgendaBinding() {}

type runtimeAgendaBinding struct{}

func runtimeBoundaryBinding() boundaryAgendaBinding {
	return runtimeAgendaBinding{}
}

func (runtimeAgendaBinding) boundaryAgendaBinding() {}

type boundaryEligibility uint8

const (
	boundaryEligibilityStep boundaryEligibility = iota + 1
	boundaryEligibilityTurn
	boundaryEligibilityIdle
	boundaryEligibilitySafe
)

type boundarySelection interface {
	boundarySelection()
}

type scopeStepBoundarySelection struct {
	scopeID runtimeids.ExecutionScopeID
	origin  serverapi.RuntimeStepOrigin
}

func stepBoundarySelection(
	scopeID runtimeids.ExecutionScopeID,
	origin serverapi.RuntimeStepOrigin,
) boundarySelection {
	return scopeStepBoundarySelection{
		scopeID: scopeID,
		origin:  origin,
	}
}

func (scopeStepBoundarySelection) boundarySelection() {}

type scopeTurnBoundarySelection struct {
	scopeID runtimeids.ExecutionScopeID
	origin  serverapi.RuntimeStepOrigin
}

func turnBoundarySelection(
	scopeID runtimeids.ExecutionScopeID,
	origin serverapi.RuntimeStepOrigin,
) boundarySelection {
	return scopeTurnBoundarySelection{
		scopeID: scopeID,
		origin:  origin,
	}
}

func (scopeTurnBoundarySelection) boundarySelection() {}

type scopeContinuationBoundarySelection struct {
	scopeID     runtimeids.ExecutionScopeID
	includeTurn bool
}

func continuationBoundarySelection(
	scopeID runtimeids.ExecutionScopeID,
	includeTurn bool,
) boundarySelection {
	return scopeContinuationBoundarySelection{
		scopeID:     scopeID,
		includeTurn: includeTurn,
	}
}

func (scopeContinuationBoundarySelection) boundarySelection() {}

type idleAgendaSelection struct{}

func idleBoundarySelection() boundarySelection {
	return idleAgendaSelection{}
}

func (idleAgendaSelection) boundarySelection() {}

type boundaryAgendaItem interface {
	agendaID() boundaryAgendaItemID
	agendaBinding() boundaryAgendaBinding
	agendaEligibility() boundaryEligibility
	agendaOrder() uint64
	setAgendaOrder(uint64)
	settleBoundaryAgenda(error)
}

type boundaryAgenda struct {
	mu        sync.RWMutex
	closed    bool
	nextOrder uint64
	entries   []boundaryAgendaItem
}

func newBoundaryAgenda() *boundaryAgenda {
	return &boundaryAgenda{}
}

func (a *boundaryAgenda) accept(item boundaryAgendaItem) error {
	if item == nil {
		return errors.New("Boundary Agenda item is required")
	}
	if item.agendaID() == "" {
		return errors.New("Boundary Agenda item identity is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errBoundaryRuntimeClosed
	}
	if err := validateBoundaryAgendaBinding(item.agendaBinding()); err != nil {
		return err
	}
	if !item.agendaEligibility().valid() {
		return errors.New("Boundary Agenda item eligibility is invalid")
	}
	if item.agendaOrder() != 0 {
		return errors.New("Boundary Agenda item already has an admission order")
	}
	for _, pending := range a.entries {
		if pending.agendaID() == item.agendaID() {
			return fmt.Errorf("Boundary Agenda item %q is already pending", item.agendaID())
		}
	}
	a.nextOrder++
	item.setAgendaOrder(a.nextOrder)
	a.entries = append(a.entries, item)
	return nil
}

func (a *boundaryAgenda) selectNext(selection boundarySelection) boundaryAgendaItem {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	for index, item := range a.entries {
		if !boundaryAgendaItemEligible(item, selection) {
			continue
		}
		a.entries = append(a.entries[:index], a.entries[index+1:]...)
		return item
	}
	return nil
}

func (a *boundaryAgenda) selectNextLong(selection boundarySelection) boundaryLongAgendaItem {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	for index, item := range a.entries {
		if !boundaryAgendaItemEligible(item, selection) {
			continue
		}
		long, ok := item.(boundaryLongAgendaItem)
		if !ok {
			return nil
		}
		a.entries = append(a.entries[:index], a.entries[index+1:]...)
		return long
	}
	return nil
}

func (a *boundaryAgenda) peekNext(selection boundarySelection) boundaryAgendaItem {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return nil
	}
	for _, item := range a.entries {
		if boundaryAgendaItemEligible(item, selection) {
			return item
		}
	}
	return nil
}

func (a *boundaryAgenda) pending() []boundaryAgendaItem {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]boundaryAgendaItem(nil), a.entries...)
}

func (a *boundaryAgenda) discard(id boundaryAgendaItemID, err error) bool {
	if a == nil || id == "" {
		return false
	}
	a.mu.Lock()
	for index, item := range a.entries {
		if item.agendaID() != id {
			continue
		}
		a.entries = append(a.entries[:index], a.entries[index+1:]...)
		a.mu.Unlock()
		item.settleBoundaryAgenda(err)
		return true
	}
	a.mu.Unlock()
	return false
}

func (a *boundaryAgenda) finalizeScope(scopeID runtimeids.ExecutionScopeID, err error) {
	if a == nil || scopeID.IsZero() {
		return
	}
	a.mu.Lock()
	remaining := a.entries[:0]
	var settled []boundaryAgendaItem
	for _, item := range a.entries {
		boundScopeID, scopeBound := boundaryAgendaBindingScope(item.agendaBinding())
		if scopeBound && boundScopeID == scopeID {
			settled = append(settled, item)
			continue
		}
		remaining = append(remaining, item)
	}
	a.entries = remaining
	a.mu.Unlock()
	for _, item := range settled {
		item.settleBoundaryAgenda(err)
	}
}

func (a *boundaryAgenda) close(err error) bool {
	if a == nil {
		return false
	}
	if err == nil {
		err = errBoundaryRuntimeClosed
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return false
	}
	a.closed = true
	pending := a.entries
	a.entries = nil
	a.mu.Unlock()
	for _, item := range pending {
		item.settleBoundaryAgenda(err)
	}
	return true
}

func (a *boundaryAgenda) isClosed() bool {
	if a == nil {
		return true
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.closed
}

type humanBoundaryAgendaItem struct {
	message     QueuedUserMessage
	binding     boundaryAgendaBinding
	eligibility boundaryEligibility
	order       uint64
	settle      func(error)
}

func (i *humanBoundaryAgendaItem) agendaID() boundaryAgendaItemID {
	return boundaryAgendaItemID(i.message.ID)
}

func (i *humanBoundaryAgendaItem) agendaBinding() boundaryAgendaBinding {
	return i.binding
}

func (i *humanBoundaryAgendaItem) agendaEligibility() boundaryEligibility {
	return i.eligibility
}

func (i *humanBoundaryAgendaItem) agendaOrder() uint64 {
	return i.order
}

func (i *humanBoundaryAgendaItem) setAgendaOrder(order uint64) {
	i.order = order
}

func (i *humanBoundaryAgendaItem) settleBoundaryAgenda(err error) {
	if i.settle != nil {
		i.settle(err)
	}
}

func (a *boundaryAgenda) acceptHuman(
	message QueuedUserMessage,
	binding boundaryAgendaBinding,
	eligibility boundaryEligibility,
	settle func(error),
) error {
	if _, err := runtimeids.ParseQueueItemID(message.ID); err != nil {
		return fmt.Errorf("human Boundary Agenda Queue Item ID: %w", err)
	}
	if _, err := message.DisplayText(); err != nil {
		return err
	}
	if settle == nil {
		return errors.New("human Boundary Agenda item settlement is required")
	}
	return a.accept(&humanBoundaryAgendaItem{
		message:     message,
		binding:     binding,
		eligibility: eligibility,
		settle:      settle,
	})
}

func (a *boundaryAgenda) pendingHuman() []QueuedUserMessage {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	pending := make([]QueuedUserMessage, 0, len(a.entries))
	for _, entry := range a.entries {
		human, ok := entry.(*humanBoundaryAgendaItem)
		if ok {
			pending = append(pending, human.message)
		}
	}
	return pending
}

func (a *boundaryAgenda) hasHumanScope(scopeID runtimeids.ExecutionScopeID) bool {
	if a == nil || scopeID.IsZero() {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, entry := range a.entries {
		human, ok := entry.(*humanBoundaryAgendaItem)
		if !ok {
			continue
		}
		boundScopeID, scopeBound := boundaryAgendaBindingScope(human.binding)
		if scopeBound && boundScopeID == scopeID {
			return true
		}
	}
	return false
}

func (a *boundaryAgenda) hasEligibleHuman(selection boundarySelection) bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return false
	}
	for _, entry := range a.entries {
		human, ok := entry.(*humanBoundaryAgendaItem)
		if ok && boundaryAgendaItemEligible(human, selection) {
			return true
		}
	}
	return false
}

func (a *boundaryAgenda) selectHuman(selection boundarySelection) []QueuedUserMessage {
	selected := a.selectHumanItems(selection)
	messages := make([]QueuedUserMessage, 0, len(selected))
	for _, item := range selected {
		messages = append(messages, item.message)
	}
	return messages
}

func (a *boundaryAgenda) selectHumanItems(selection boundarySelection) []*humanBoundaryAgendaItem {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	selected := make([]*humanBoundaryAgendaItem, 0)
	remaining := a.entries[:0]
	for _, entry := range a.entries {
		human, ok := entry.(*humanBoundaryAgendaItem)
		if ok && boundaryAgendaItemEligible(human, selection) {
			selected = append(selected, human)
			continue
		}
		remaining = append(remaining, entry)
	}
	a.entries = remaining
	return selected
}

func (a *boundaryAgenda) selectHumanPrefix(selection boundarySelection) []*humanBoundaryAgendaItem {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	selected := make([]*humanBoundaryAgendaItem, 0)
	remaining := make([]boundaryAgendaItem, 0, len(a.entries))
	reachedAnotherFamily := false
	for _, entry := range a.entries {
		if reachedAnotherFamily || !boundaryAgendaItemEligible(entry, selection) {
			remaining = append(remaining, entry)
			continue
		}
		human, ok := entry.(*humanBoundaryAgendaItem)
		if !ok {
			reachedAnotherFamily = true
			remaining = append(remaining, entry)
			continue
		}
		selected = append(selected, human)
	}
	a.entries = remaining
	return selected
}

func (a *boundaryAgenda) selectAllHumanItems() []*humanBoundaryAgendaItem {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	selected := make([]*humanBoundaryAgendaItem, 0)
	remaining := a.entries[:0]
	for _, entry := range a.entries {
		human, ok := entry.(*humanBoundaryAgendaItem)
		if ok {
			selected = append(selected, human)
			continue
		}
		remaining = append(remaining, entry)
	}
	a.entries = remaining
	return selected
}

func (a *boundaryAgenda) restoreHumanFront(items []*humanBoundaryAgendaItem) {
	if a == nil || len(items) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	restored := make([]boundaryAgendaItem, 0, len(items)+len(a.entries))
	for _, item := range items {
		if item != nil {
			restored = append(restored, item)
		}
	}
	a.entries = append(a.entries, restored...)
	sort.SliceStable(a.entries, func(left, right int) bool {
		return a.entries[left].agendaOrder() < a.entries[right].agendaOrder()
	})
}

func (a *boundaryAgenda) takeHuman(id runtimeids.QueueItemID) (*humanBoundaryAgendaItem, bool) {
	if a == nil || id.IsZero() {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for index, entry := range a.entries {
		human, ok := entry.(*humanBoundaryAgendaItem)
		if !ok || human.message.ID != id.String() {
			continue
		}
		a.entries = append(a.entries[:index], a.entries[index+1:]...)
		return human, true
	}
	return nil, false
}

func (a *boundaryAgenda) takeHumanIDs(ids map[string]struct{}) []*humanBoundaryAgendaItem {
	if a == nil || len(ids) == 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	selected := make([]*humanBoundaryAgendaItem, 0, len(ids))
	remaining := a.entries[:0]
	for _, entry := range a.entries {
		human, ok := entry.(*humanBoundaryAgendaItem)
		if ok {
			if _, selectedID := ids[human.message.ID]; selectedID {
				selected = append(selected, human)
				continue
			}
		}
		remaining = append(remaining, entry)
	}
	a.entries = remaining
	return selected
}

func (a *boundaryAgenda) takeHumanScope(scopeID runtimeids.ExecutionScopeID) []*humanBoundaryAgendaItem {
	if a == nil || scopeID.IsZero() {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	selected := make([]*humanBoundaryAgendaItem, 0)
	remaining := a.entries[:0]
	for _, entry := range a.entries {
		human, ok := entry.(*humanBoundaryAgendaItem)
		if ok {
			boundScopeID, scopeBound := boundaryAgendaBindingScope(human.binding)
			if scopeBound && boundScopeID == scopeID {
				selected = append(selected, human)
				continue
			}
		}
		remaining = append(remaining, entry)
	}
	a.entries = remaining
	return selected
}

func (a *boundaryAgenda) consumeBackgroundSession(sessionID string) bool {
	if a == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	a.mu.Lock()
	for index, entry := range a.entries {
		notice, ok := entry.(*backgroundNoticeAgendaItem)
		if !ok || notice.sessionID != sessionID {
			continue
		}
		a.entries = append(a.entries[:index], a.entries[index+1:]...)
		a.mu.Unlock()
		notice.settleBoundaryAgenda(nil)
		return true
	}
	a.mu.Unlock()
	return false
}

func validateBoundaryAgendaBinding(binding boundaryAgendaBinding) error {
	switch typed := binding.(type) {
	case scopeAgendaBinding:
		if typed.scopeID.IsZero() {
			return errors.New("scope-bound Boundary Agenda item requires an Exact Execution Scope")
		}
		if err := typed.origin.Validate(); err != nil {
			return fmt.Errorf("scope-bound Boundary Agenda item origin: %w", err)
		}
		return nil
	case continuationScopeAgendaBinding:
		if typed.scopeID.IsZero() {
			return errors.New("continuation scope Boundary Agenda item requires an Exact Execution Scope")
		}
		return nil
	case runtimeAgendaBinding:
		return nil
	default:
		return errors.New("Boundary Agenda binding kind is invalid")
	}
}

func (e boundaryEligibility) valid() bool {
	switch e {
	case boundaryEligibilityStep,
		boundaryEligibilityTurn,
		boundaryEligibilityIdle,
		boundaryEligibilitySafe:
		return true
	default:
		return false
	}
}

func boundaryAgendaItemEligible(item boundaryAgendaItem, selection boundarySelection) bool {
	if item == nil || selection == nil {
		return false
	}
	binding := item.agendaBinding()
	switch selected := selection.(type) {
	case scopeStepBoundarySelection:
		if _, runtimeBound := binding.(runtimeAgendaBinding); runtimeBound {
			return item.agendaEligibility() == boundaryEligibilitySafe
		}
		scope, scopeBound := binding.(scopeAgendaBinding)
		return scopeBound &&
			item.agendaEligibility() == boundaryEligibilityStep &&
			scope.scopeID == selected.scopeID &&
			scope.origin == selected.origin
	case scopeTurnBoundarySelection:
		if _, runtimeBound := binding.(runtimeAgendaBinding); runtimeBound {
			return item.agendaEligibility() == boundaryEligibilitySafe
		}
		scope, scopeBound := binding.(scopeAgendaBinding)
		return scopeBound &&
			(item.agendaEligibility() == boundaryEligibilityStep ||
				item.agendaEligibility() == boundaryEligibilityTurn) &&
			scope.scopeID == selected.scopeID &&
			scope.origin == selected.origin
	case scopeContinuationBoundarySelection:
		if _, runtimeBound := binding.(runtimeAgendaBinding); runtimeBound {
			return item.agendaEligibility() == boundaryEligibilitySafe
		}
		scopeID, scopeBound := boundaryAgendaBindingScope(binding)
		return scopeBound &&
			scopeID == selected.scopeID &&
			(item.agendaEligibility() == boundaryEligibilityStep ||
				(selected.includeTurn &&
					item.agendaEligibility() == boundaryEligibilityTurn))
	case idleAgendaSelection:
		_, runtimeBound := binding.(runtimeAgendaBinding)
		return runtimeBound &&
			(item.agendaEligibility() == boundaryEligibilityIdle ||
				item.agendaEligibility() == boundaryEligibilitySafe)
	default:
		return false
	}
}

func boundaryAgendaBindingScope(
	binding boundaryAgendaBinding,
) (runtimeids.ExecutionScopeID, bool) {
	switch typed := binding.(type) {
	case scopeAgendaBinding:
		return typed.scopeID, true
	case continuationScopeAgendaBinding:
		return typed.scopeID, true
	default:
		return runtimeids.ExecutionScopeID{}, false
	}
}
