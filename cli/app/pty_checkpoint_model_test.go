package app

import (
	"fmt"
	"sync"

	checkpoint "core/internal/testharness/pty/analyzer"
	"core/internal/testharness/pty/appfixture"
	"core/shared/clientui"
	"core/shared/transcript"

	tea "github.com/charmbracelet/bubbletea"
)

type ptyCheckpointModel struct {
	inner                    tea.Model
	output                   *checkpoint.Writer
	scenario                 *ptyCheckpointScenarioState
	detailPageAppliedPending bool
}

type ptyCheckpointScenarioState struct {
	mu                           sync.Mutex
	targetFinalAssistantOrdinal  appfixture.ScriptFinalAssistantOrdinal
	appliedFinalAssistantOrdinal appfixture.ScriptFinalAssistantOrdinal
	targetFinalSequence          *uint64
	scenarioComplete             bool
	finalAppliedEmitted          bool
}

func newPTYCheckpointScenarioState(
	targetFinalAssistantOrdinal appfixture.ScriptFinalAssistantOrdinal,
) *ptyCheckpointScenarioState {
	if targetFinalAssistantOrdinal == 0 {
		panic("create PTY checkpoint scenario state with invalid target final assistant ordinal")
	}
	return &ptyCheckpointScenarioState{
		targetFinalAssistantOrdinal: targetFinalAssistantOrdinal,
	}
}

func (state *ptyCheckpointScenarioState) markScenarioComplete() {
	if state == nil {
		panic("mark scenario complete on nil PTY checkpoint scenario state")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.scenarioComplete {
		panic("mark PTY checkpoint scenario complete more than once")
	}
	if state.appliedFinalAssistantOrdinal >= state.targetFinalAssistantOrdinal {
		panic(fmt.Sprintf(
			"mark PTY checkpoint scenario complete after target final was applied: applied_ordinal=%d target_ordinal=%d",
			state.appliedFinalAssistantOrdinal,
			state.targetFinalAssistantOrdinal,
		))
	}
	state.scenarioComplete = true
}

func (state *ptyCheckpointScenarioState) recordAcceptedAssistantFinal(sequence uint64) bool {
	if state == nil {
		panic("record accepted assistant final on nil PTY checkpoint scenario state")
	}
	if sequence == 0 {
		panic("record accepted assistant final with invalid transcript sequence")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.appliedFinalAssistantOrdinal++
	if state.appliedFinalAssistantOrdinal != state.targetFinalAssistantOrdinal {
		return false
	}
	sequenceCopy := sequence
	state.targetFinalSequence = &sequenceCopy
	return true
}

func (state *ptyCheckpointScenarioState) pendingTargetFinalSequence() (uint64, bool) {
	if state == nil {
		panic("read pending target final sequence on nil PTY checkpoint scenario state")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.scenarioComplete ||
		state.finalAppliedEmitted ||
		state.targetFinalSequence == nil {
		return 0, false
	}
	return *state.targetFinalSequence, true
}

func (state *ptyCheckpointScenarioState) claimScenarioFinalApplied(sequence uint64) bool {
	if state == nil {
		panic("claim scenario final applied on nil PTY checkpoint scenario state")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.finalAppliedEmitted ||
		!state.scenarioComplete ||
		state.targetFinalSequence == nil ||
		*state.targetFinalSequence != sequence {
		return false
	}
	state.finalAppliedEmitted = true
	return true
}

func newPTYCheckpointModel(
	inner tea.Model,
	output *checkpoint.Writer,
	scenario *ptyCheckpointScenarioState,
) *ptyCheckpointModel {
	if inner == nil {
		panic("create PTY checkpoint model with nil inner model")
	}
	if output == nil {
		panic("create PTY checkpoint model with nil checkpoint writer")
	}
	if scenario == nil {
		panic("create PTY checkpoint model with nil scenario state")
	}
	return &ptyCheckpointModel{inner: inner, output: output, scenario: scenario}
}

func (model *ptyCheckpointModel) Init() tea.Cmd {
	return model.inner.Init()
}

func (model *ptyCheckpointModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	initialDetailLoad := initialDetailLoadCandidate(model.inner, msg)
	ongoingFinal := ongoingAssistantFinalCandidate(model.inner, msg)
	queuedTargetFinal := ongoingTargetFinalDrainCandidate(model.inner, msg, model.scenario)
	next, cmd := model.inner.Update(msg)
	if next == nil {
		panic(fmt.Sprintf("PTY checkpoint inner model returned nil: message_type=%T", msg))
	}
	model.inner = next
	if initialDetailLoad.appliedBy(next) {
		model.detailPageAppliedPending = true
	}
	emitScenarioFinalApplied := false
	if ongoingFinal.acceptedBy(next) &&
		model.scenario.recordAcceptedAssistantFinal(ongoingFinal.sequence) &&
		ongoingFinal.terminalAppliedBy(next) {
		emitScenarioFinalApplied = model.scenario.claimScenarioFinalApplied(ongoingFinal.sequence)
	}
	if queuedTargetFinal.terminalAppliedBy(next) {
		emitScenarioFinalApplied = model.scenario.claimScenarioFinalApplied(queuedTargetFinal.sequence)
	}
	if emitScenarioFinalApplied {
		model.emitScenarioFinalApplied()
	}
	if isTerminalInputMessage(msg) {
		if err := model.output.Emit(checkpoint.KindInputApplied, nil); err != nil {
			panic(fmt.Sprintf("emit PTY input-applied checkpoint: message_type=%T error=%v", msg, err))
		}
	}
	return model, cmd
}

func (model *ptyCheckpointModel) emitScenarioFinalApplied() {
	if err := model.output.Emit(checkpoint.KindScenarioFinalApplied, nil); err != nil {
		panic(fmt.Sprintf("emit PTY scenario-final-applied checkpoint: error=%v", err))
	}
}

type ptyOngoingAssistantFinalCandidate struct {
	sequence          uint64
	terminalImmediate bool
	valid             bool
}

func ongoingAssistantFinalCandidate(model tea.Model, msg tea.Msg) ptyOngoingAssistantFinalCandidate {
	event, ok := msg.(ongoingTranscriptEvent)
	if !ok ||
		event.Kind != ongoingTranscriptEventMessage ||
		!isCommittedAssistantFinal(event.Message) {
		return ptyOngoingAssistantFinalCandidate{}
	}
	appModel, ok := model.(*uiModel)
	if !ok ||
		appModel.ongoingTranscript == nil ||
		!appModel.ongoingTranscript.hydrated ||
		event.Message.Sequence != appModel.ongoingTranscript.lastSequence+1 {
		return ptyOngoingAssistantFinalCandidate{}
	}
	return ptyOngoingAssistantFinalCandidate{
		sequence:          event.Message.Sequence,
		terminalImmediate: appModel.ongoingTranscript.normalOwned && appModel.nativeOngoingSurfaceActive(),
		valid:             true,
	}
}

func (candidate ptyOngoingAssistantFinalCandidate) acceptedBy(model tea.Model) bool {
	if !candidate.valid {
		return false
	}
	appModel, ok := model.(*uiModel)
	return ok &&
		!appModel.forcedLocalExit &&
		appModel.ongoingTranscript != nil &&
		appModel.ongoingTranscript.lastSequence == candidate.sequence
}

func (candidate ptyOngoingAssistantFinalCandidate) terminalAppliedBy(model tea.Model) bool {
	if !candidate.terminalImmediate || !candidate.acceptedBy(model) {
		return false
	}
	appModel := model.(*uiModel)
	return appModel.ongoingTranscript.normalOwned && appModel.nativeOngoingSurfaceActive()
}

type ptyOngoingTargetFinalDrainCandidate struct {
	sequence uint64
	valid    bool
}

func ongoingTargetFinalDrainCandidate(
	model tea.Model,
	msg tea.Msg,
	scenario *ptyCheckpointScenarioState,
) ptyOngoingTargetFinalDrainCandidate {
	ownership, ok := msg.(ongoingNormalBufferOwnedMsg)
	if !ok || !ownership.owned {
		return ptyOngoingTargetFinalDrainCandidate{}
	}
	targetSequence, ok := scenario.pendingTargetFinalSequence()
	if !ok {
		return ptyOngoingTargetFinalDrainCandidate{}
	}
	appModel, ok := model.(*uiModel)
	if !ok ||
		appModel.ongoingTranscript == nil ||
		appModel.ongoingTranscript.normalOwned ||
		appModel.ongoingTranscript.queueOverflowed ||
		!appModel.nativeOngoingSurfaceActive() {
		return ptyOngoingTargetFinalDrainCandidate{}
	}
	for _, message := range appModel.ongoingTranscript.queue {
		if message.Sequence == targetSequence && isCommittedAssistantFinal(message) {
			return ptyOngoingTargetFinalDrainCandidate{
				sequence: targetSequence,
				valid:    true,
			}
		}
	}
	return ptyOngoingTargetFinalDrainCandidate{}
}

func (candidate ptyOngoingTargetFinalDrainCandidate) terminalAppliedBy(model tea.Model) bool {
	if !candidate.valid {
		return false
	}
	appModel, ok := model.(*uiModel)
	return ok &&
		!appModel.forcedLocalExit &&
		appModel.ongoingTranscript != nil &&
		appModel.ongoingTranscript.normalOwned &&
		appModel.nativeOngoingSurfaceActive() &&
		!appModel.ongoingTranscript.queueOverflowed &&
		len(appModel.ongoingTranscript.queue) == 0 &&
		appModel.ongoingTranscript.lastSequence >= candidate.sequence
}

func isCommittedAssistantFinal(message clientui.TranscriptMessage) bool {
	if message.Kind != clientui.TranscriptMessageCommittedRow ||
		message.CommittedRow == nil ||
		message.CommittedRow.Kind != clientui.TranscriptRowAssistant ||
		message.CommittedRow.Assistant == nil {
		return false
	}
	switch message.CommittedRow.Assistant.Phase {
	case transcript.AssistantPhaseFinal, transcript.AssistantPhaseLegacyFinal:
		return true
	default:
		return false
	}
}

func (model *ptyCheckpointModel) View() string {
	view := model.inner.View()
	if !model.detailPageAppliedPending || view == "" {
		return view
	}
	if err := model.output.QueueBeforeNextWrite(checkpoint.KindDetailInitialPageApplied, nil); err != nil {
		panic(fmt.Sprintf("queue PTY detail-page-applied checkpoint: error=%v", err))
	}
	model.detailPageAppliedPending = false
	return view
}

func (model *ptyCheckpointModel) appModel() (*uiModel, bool) {
	inner, ok := model.inner.(*uiModel)
	return inner, ok
}

func isTerminalInputMessage(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyMsg, tea.MouseMsg, customKeyMsg:
		return true
	default:
		return false
	}
}

type ptyInitialDetailLoadCandidate struct {
	message detailTranscriptLoadMsg
	valid   bool
}

func initialDetailLoadCandidate(model tea.Model, msg tea.Msg) ptyInitialDetailLoadCandidate {
	load, ok := msg.(detailTranscriptLoadMsg)
	if !ok || load.err != nil {
		return ptyInitialDetailLoadCandidate{}
	}
	appModel, ok := model.(*uiModel)
	if !ok ||
		appModel.pendingDetailTranscript == nil ||
		appModel.pendingDetailTranscript.id != load.requestID ||
		appModel.detailTranscript.loaded {
		return ptyInitialDetailLoadCandidate{}
	}
	return ptyInitialDetailLoadCandidate{message: load, valid: true}
}

func (candidate ptyInitialDetailLoadCandidate) appliedBy(model tea.Model) bool {
	if !candidate.valid {
		return false
	}
	appModel, ok := model.(*uiModel)
	if !ok ||
		appModel.surface() != uiSurfaceTranscriptDetail ||
		appModel.pendingDetailTranscript != nil ||
		!appModel.detailTranscript.loaded {
		return false
	}
	return appModel.detailTranscript.matchesPage(candidate.message.page)
}
