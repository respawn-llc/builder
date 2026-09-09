package app

import (
	"time"

	"core/cli/tui"
	tuiinput "core/cli/tui/input"
	"core/shared/clientui"
)

type uiInputMode string

const (
	uiInputModeMain              uiInputMode = "main"
	uiInputModeAsk               uiInputMode = "ask"
	uiInputModeStatus            uiInputMode = "status"
	uiInputModeGoal              uiInputMode = "goal"
	uiInputModeWorktree          uiInputMode = "worktree"
	uiInputModeRollbackSelection uiInputMode = "rollback_selection"
	uiInputModeProcessList       uiInputMode = "process_list"
)

type uiInteractionState struct {
	Mode uiInputMode
}

type uiAskState struct {
	current                 *askEvent
	currentToken            uint64
	queue                   []askEvent
	cursor                  int
	freeform                bool
	freeformMode            askFreeformMode
	activeDelivery          *activePromptAnswerDelivery
	editor                  tuiinput.Editor
	activeProjection        *activeQuestionProjection
	inFlightProjection      *questionRenderRequest
	latestDesiredProjection *desiredQuestionProjection
}

type uiProcessListState struct {
	open              bool
	selection         int
	entries           []clientui.BackgroundProcess
	loading           bool
	errorText         string
	refreshToken      uint64
	refreshInFlight   bool
	refreshDirty      bool
	actionToken       uint64
	actionInFlight    bool
	surfaceGeneration uint64
}

type uiRollbackPhase string

const (
	uiRollbackPhaseInactive               uiRollbackPhase = "inactive"
	uiRollbackPhaseAwaitingNewest         uiRollbackPhase = "awaiting_newest"
	uiRollbackPhaseAwaitingOlderCandidate uiRollbackPhase = "awaiting_older_candidate"
	uiRollbackPhaseSelection              uiRollbackPhase = "selection"
)

type rollbackCandidate struct {
	RollbackTargetID string
	Text             string
}

type uiRollbackPageNavigation struct {
	direction                tui.DetailTranscriptPageDirection
	anchorRollbackTargetID   string
	request                  clientui.TranscriptPageRequest
	deadline                 time.Time
	skippedCandidateFreePage bool
}

type uiRollbackState struct {
	phase                     uiRollbackPhase
	restoreTranscriptMode     *tui.Mode
	restoreDetailTranscript   *uiDetailTranscriptWindow
	restoreDetailPresentation *tui.DetailPresentationSnapshot
	candidates                []rollbackCandidate
	selectedTargetID          *string
	activationTargetID        *string
	pendingNavigation         *uiRollbackPageNavigation
}

type uiStatusOverlayState struct {
	open            bool
	loading         bool
	scroll          int
	snapshot        uiStatusSnapshot
	error           string
	refreshToken    uint64
	pendingSections map[uiStatusSection]bool
	sectionWarnings map[uiStatusSection]string
}

type uiGoalOverlayState struct {
	open             bool
	scroll           int
	goal             *clientui.Goal
	confirmMode      string
	confirmSelection int
	pendingObjective string
	error            string
}

func (s uiAskState) hasCurrent() bool {
	return s.current != nil
}

func (s uiRollbackState) isSelecting() bool {
	return s.phase == uiRollbackPhaseSelection
}

func (s uiRollbackState) isActive() bool {
	return s.isSelecting()
}

func (s uiRollbackState) isAwaitingNewest() bool {
	return s.phase == uiRollbackPhaseAwaitingNewest
}

func (s uiRollbackState) isAwaitingOlderCandidate() bool {
	return s.phase == uiRollbackPhaseAwaitingOlderCandidate
}

func (s uiRollbackState) isAwaitingActivation() bool {
	return s.isAwaitingNewest() || s.isAwaitingOlderCandidate()
}

type uiInputModeState struct {
	Mode           uiInputMode
	Busy           bool
	ShowsMainInput bool
	ShowsAskInput  bool
}

func (m *uiModel) inputMode() uiInputMode {
	if m == nil || m.interaction.Mode == "" {
		return uiInputModeMain
	}
	return m.interaction.Mode
}

func (m *uiModel) setInputMode(mode uiInputMode) {
	if m == nil {
		return
	}
	if mode == "" {
		mode = uiInputModeMain
	}
	m.interaction.Mode = mode
}

func (m *uiModel) restorePrimaryInputMode() {
	if m == nil {
		return
	}
	if m.ask.hasCurrent() && (m.view.Mode() == "" || m.view.Mode() == tui.ModeOngoing) {
		m.setInputMode(uiInputModeAsk)
		m.notifyPendingTranscriptPromptActivation()
		return
	}
	m.setInputMode(uiInputModeMain)
}

func (m *uiModel) inputModeState() uiInputModeState {
	mode := m.inputMode()
	if mode == uiInputModeAsk && !m.askReadyForInteraction() {
		return uiInputModeState{
			Mode: mode,
			Busy: m != nil && m.isBusy(),
		}
	}
	return uiInputModeState{
		Mode:           mode,
		Busy:           m != nil && m.isBusy(),
		ShowsMainInput: mode.showsMainInput(),
		ShowsAskInput:  mode.showsAskInput(),
	}
}

func (m *uiModel) askReadyForInteraction() bool {
	return m != nil && m.ask.hasCurrent() && m.ask.activeProjection != nil
}

func (mode uiInputMode) showsMainInput() bool {
	return mode == uiInputModeMain
}

func (mode uiInputMode) showsAskInput() bool {
	return mode == uiInputModeAsk
}
