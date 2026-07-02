package app

import (
	"core/cli/tui"
	"core/shared/clientui"

	"github.com/charmbracelet/glamour"
)

type uiInputMode string

const (
	uiInputModeMain              uiInputMode = "main"
	uiInputModeAsk               uiInputMode = "ask"
	uiInputModeStatus            uiInputMode = "status"
	uiInputModeGoal              uiInputMode = "goal"
	uiInputModeWorktree          uiInputMode = "worktree"
	uiInputModeRollbackSelection uiInputMode = "rollback_selection"
	uiInputModeRollbackEdit      uiInputMode = "rollback_edit"
	uiInputModeProcessList       uiInputMode = "process_list"
)

type uiInteractionState struct {
	Mode uiInputMode
}

type uiAskState struct {
	current       *askEvent
	currentToken  uint64
	queue         []askEvent
	cursor        int
	freeform      bool
	freeformMode  askFreeformMode
	answerPending bool
	input         string
	inputCursor   int
	inputKill     string
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
	uiRollbackPhaseInactive  uiRollbackPhase = "inactive"
	uiRollbackPhaseSelection uiRollbackPhase = "selection"
	uiRollbackPhaseEditing   uiRollbackPhase = "editing"
)

type uiRollbackState struct {
	phase                     uiRollbackPhase
	suppressedAlternateScroll bool
	restoreTranscriptMode     tui.Mode
	candidates                []rollbackCandidate
	selection                 int
	selectedTranscriptEntry   int
	selectedTargetID          string
	pendingSelectionAnchor    int
	pendingSelectionDelta     int
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
	goal             *clientui.RuntimeGoal
	confirmMode      string
	confirmSelection int
	pendingObjective string
	error            string
	markdownTheme    string
	markdownWidth    int
	markdownRenderer *glamour.TermRenderer
}

func (s uiAskState) hasCurrent() bool {
	return s.current != nil
}

func (s uiRollbackState) isSelecting() bool {
	return s.phase == uiRollbackPhaseSelection
}

func (s uiRollbackState) isEditing() bool {
	return s.phase == uiRollbackPhaseEditing
}

func (s uiRollbackState) isActive() bool {
	return s.phase != uiRollbackPhaseInactive
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
		return
	}
	m.setInputMode(uiInputModeMain)
}

func (m *uiModel) inputModeState() uiInputModeState {
	mode := m.inputMode()
	return uiInputModeState{
		Mode:           mode,
		Busy:           m != nil && m.isBusy(),
		ShowsMainInput: mode.showsMainInput(),
		ShowsAskInput:  mode.showsAskInput(),
	}
}

func (mode uiInputMode) showsMainInput() bool {
	return mode == uiInputModeMain || mode == uiInputModeRollbackEdit
}

func (mode uiInputMode) showsAskInput() bool {
	return mode == uiInputModeAsk
}
