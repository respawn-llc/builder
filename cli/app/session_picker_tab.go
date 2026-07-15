package app

import (
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

type sessionPickerBodyPhase string

const (
	sessionPickerBodyInitialLoading sessionPickerBodyPhase = "initial_loading"
	sessionPickerBodyReady          sessionPickerBodyPhase = "ready"
	sessionPickerBodyEmpty          sessionPickerBodyPhase = "empty"
	sessionPickerBodyFailed         sessionPickerBodyPhase = "failed"
)

type sessionPickerBodyRequestKind uint8

const (
	sessionPickerBodyRequestInitial sessionPickerBodyRequestKind = iota + 1
	sessionPickerBodyRequestRetry
)

type sessionPickerBodyRequest struct {
	kind       sessionPickerBodyRequestKind
	generation uint64
	position   serverapi.SessionPagePosition
}

type sessionPickerDirectionalRequest struct {
	generation uint64
	position   serverapi.SessionPagePosition
	move       int
}

type sessionPickerPageSegment struct {
	sessions []clientui.SessionSummary
	older    *serverapi.SessionPageContinuation
	newer    *serverapi.SessionPageContinuation
}

type sessionPickerSelection interface {
	isSessionPickerSelection()
}

type sessionPickerCreateSelection struct{}
type sessionPickerSessionSelection struct {
	sessionID runtimeids.SessionID
}

func (sessionPickerCreateSelection) isSessionPickerSelection()  {}
func (sessionPickerSessionSelection) isSessionPickerSelection() {}

func newSessionPickerSessionSelection(sessionID runtimeids.SessionID) sessionPickerSessionSelection {
	if sessionID.IsZero() {
		panic("session picker selection requires a session ID")
	}
	return sessionPickerSessionSelection{sessionID: sessionID}
}

type sessionPickerTab struct {
	category sessioncontract.SessionCategory

	bodyPhase   sessionPickerBodyPhase
	bodyRequest *sessionPickerBodyRequest
	directional *sessionPickerDirectionalRequest
	generation  uint64

	segments    []sessionPickerPageSegment
	residentIDs map[runtimeids.SessionID]struct{}
	selected    sessionPickerSelection
	offset      int

	selectedDetail sessionPickerSelectedDetail
	detailRequest  *sessionPickerDetailRequest
}

func newSessionPickerTab(category sessioncontract.SessionCategory) sessionPickerTab {
	tab := sessionPickerTab{
		category:    category,
		bodyPhase:   sessionPickerBodyInitialLoading,
		residentIDs: make(map[runtimeids.SessionID]struct{}),
	}
	if category == sessioncontract.SessionCategoryMain {
		tab.selected = sessionPickerCreateSelection{}
	}
	return tab
}

func (t *sessionPickerTab) residentSessionCount() int {
	return len(t.residentIDs)
}

func (t *sessionPickerTab) sessions() []clientui.SessionSummary {
	count := 0
	for _, segment := range t.segments {
		count += len(segment.sessions)
	}
	sessions := make([]clientui.SessionSummary, 0, count)
	for _, segment := range t.segments {
		sessions = append(sessions, segment.sessions...)
	}
	return sessions
}

func (t *sessionPickerTab) containsNewestEdge() bool {
	return len(t.segments) == 0 || t.segments[0].newer == nil
}

func (t *sessionPickerTab) includesCreateRow() bool {
	return t.category == sessioncontract.SessionCategoryMain && t.containsNewestEdge()
}

func (t *sessionPickerTab) itemCount() int {
	count := t.residentSessionCount()
	if t.includesCreateRow() {
		count++
	}
	return count
}

func (t *sessionPickerTab) selectedIndex() *int {
	if _, ok := t.selected.(sessionPickerCreateSelection); ok {
		if t.includesCreateRow() {
			index := 0
			return &index
		}
		return nil
	}
	selected, ok := t.selected.(sessionPickerSessionSelection)
	if !ok {
		return nil
	}
	index := 0
	if t.includesCreateRow() {
		index++
	}
	for _, summary := range t.sessions() {
		if summary.SessionID == selected.sessionID {
			selectedIndex := index
			return &selectedIndex
		}
		index++
	}
	return nil
}

func (t *sessionPickerTab) selectIndex(index int) {
	if index < 0 || index >= t.itemCount() {
		return
	}
	if t.includesCreateRow() {
		if index == 0 {
			t.selected = sessionPickerCreateSelection{}
			return
		}
		index--
	}
	sessions := t.sessions()
	t.selected = newSessionPickerSessionSelection(sessions[index].SessionID)
}

func (t *sessionPickerTab) resetForFreshLoad() {
	t.bodyPhase = sessionPickerBodyInitialLoading
	t.directional = nil
	t.segments = nil
	t.residentIDs = make(map[runtimeids.SessionID]struct{})
	t.offset = 0
	t.selected = nil
	if t.category == sessioncontract.SessionCategoryMain {
		t.selected = sessionPickerCreateSelection{}
	}
}

func (t *sessionPickerTab) replaceSegments(response serverapi.SessionPageResponse) {
	t.segments = []sessionPickerPageSegment{newSessionPickerPageSegment(response)}
	t.rebuildResidentIDs()
	if len(response.Sessions) == 0 {
		t.segments = nil
		t.rebuildResidentIDs()
		t.bodyPhase = sessionPickerBodyEmpty
		if t.category == sessioncontract.SessionCategoryMain {
			t.selected = sessionPickerCreateSelection{}
		} else {
			t.selected = nil
		}
		return
	}
	t.bodyPhase = sessionPickerBodyReady
}

func (t *sessionPickerTab) rebuildResidentIDs() {
	t.residentIDs = make(map[runtimeids.SessionID]struct{})
	for segmentIndex := range t.segments {
		filtered := t.segments[segmentIndex].sessions[:0]
		for _, summary := range t.segments[segmentIndex].sessions {
			if _, exists := t.residentIDs[summary.SessionID]; exists {
				continue
			}
			t.residentIDs[summary.SessionID] = struct{}{}
			filtered = append(filtered, summary)
		}
		t.segments[segmentIndex].sessions = filtered
	}
}

func newSessionPickerPageSegment(response serverapi.SessionPageResponse) sessionPickerPageSegment {
	return sessionPickerPageSegment{
		sessions: append([]clientui.SessionSummary(nil), response.Sessions...),
		older:    cloneSessionPageContinuation(response.Older),
		newer:    cloneSessionPageContinuation(response.Newer),
	}
}

func cloneSessionPageContinuation(value *serverapi.SessionPageContinuation) *serverapi.SessionPageContinuation {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func sessionPagePositionsEqual(left, right serverapi.SessionPagePosition) bool {
	if left.Kind() != right.Kind() {
		return false
	}
	leftToken, leftPresent := left.Continuation()
	rightToken, rightPresent := right.Continuation()
	return leftPresent == rightPresent && (!leftPresent || leftToken.String() == rightToken.String())
}
