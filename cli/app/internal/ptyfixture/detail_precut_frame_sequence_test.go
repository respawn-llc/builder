package ptyfixture

import (
	"slices"
	"testing"

	"core/internal/testharness/pty"
)

const (
	detailPrecutInteriorTravelInputs = 6
	detailPrecutShellTravelInputs    = 2
	detailPrecutMarkdownTravelInputs = 4

	detailFrameShiftTab = "\x1b[Z"
	detailFrameTab      = "\t"
	detailFrameUp       = "\x1b[A"
	detailFrameDown     = "\x1b[B"
	detailFrameWheelUp  = "\x1b[<64;1;1M"
	detailFrameEnter    = "\r"
	detailFrameExit     = "\x03\x03"
)

type detailPrecutFramePlan struct {
	inputs                         []pty.FrameInput
	initialNewestInputIndex        int
	newerTraversalInputIndices     []int
	afterNewerExhaustionInputIndex int
	selectedShellNextInputIndex    int
	selectedMarkdownExitInputIndex int
	finalOngoingExitInputIndex     int
}

type detailPrecutFrameCheckpoints struct {
	initialNewest      pty.ScreenSnapshot
	centerOwned        pty.ScreenSnapshot
	returnedNewest     pty.ScreenSnapshot
	newerEdgeExhausted pty.ScreenSnapshot
	selectedShell      pty.ScreenSnapshot
	selectedMarkdown   pty.ScreenSnapshot
	finalOngoing       pty.ScreenSnapshot
	ongoingBefore      pty.ScreenSnapshot
	ongoingAfter       pty.ScreenSnapshot
}

type detailPrecutProofScreen struct {
	name   string
	screen pty.ScreenSnapshot
}

type detailSelectedRowSnapshot struct {
	index int
	cells []pty.Cell
}

type detailSelectionSnapshot struct {
	rows []detailSelectedRowSnapshot
}

func newDetailPrecutFramePlan() detailPrecutFramePlan {
	plan := detailPrecutFramePlan{}
	appendInput := func(payload string, readiness pty.ReadinessBoundaryKind) int {
		index := len(plan.inputs)
		plan.inputs = append(plan.inputs, pty.FrameInput{
			Readiness: readiness,
			Bytes:     []byte(payload),
		})
		return index
	}
	appendRepeated := func(payload string, count int) {
		for range count {
			appendInput(payload, pty.ReadinessRendererFrame)
		}
	}

	appendInput(detailFrameShiftTab, pty.ReadinessRendererFrame)
	plan.initialNewestInputIndex = appendInput(detailFrameUp, pty.ReadinessRendererFrame)
	detailPageAppliedPhase := pty.PhaseDetailInitialPageApplied
	plan.inputs[plan.initialNewestInputIndex].AfterPhase = &detailPageAppliedPhase
	appendRepeated(detailFrameUp, detailPrecutInteriorTravelInputs-1)
	for range detailPrecutInteriorTravelInputs {
		plan.newerTraversalInputIndices = append(
			plan.newerTraversalInputIndices,
			appendInput(detailFrameDown, pty.ReadinessRendererFrame),
		)
	}
	plan.newerTraversalInputIndices = append(
		plan.newerTraversalInputIndices,
		appendInput(detailFrameDown, pty.ReadinessRendererFrame),
	)
	plan.afterNewerExhaustionInputIndex = appendInput(detailFrameUp, pty.ReadinessInputApplied)
	appendRepeated(detailFrameUp, detailPrecutShellTravelInputs-1)
	plan.selectedShellNextInputIndex = appendInput(detailFrameWheelUp, pty.ReadinessRendererFrame)
	appendRepeated(detailFrameWheelUp, detailPrecutMarkdownTravelInputs-1)
	appendInput(detailFrameEnter, pty.ReadinessRendererFrame)
	plan.selectedMarkdownExitInputIndex = appendInput(detailFrameTab, pty.ReadinessRendererFrame)
	plan.finalOngoingExitInputIndex = appendInput(detailFrameExit, pty.ReadinessNormalBufferRestored)
	return plan
}

func (plan detailPrecutFramePlan) sequence() pty.FrameInputSequence {
	return pty.FrameInputSequence{
		Phase:  pty.PhaseScenarioComplete,
		Inputs: plan.inputs,
	}
}

func collectDetailPrecutFrameCheckpoints(t *testing.T, capture pty.Capture, plan detailPrecutFramePlan, bounds detailAlternateScreenOffsets) detailPrecutFrameCheckpoints {
	t.Helper()
	dispatches := detailPrecutFrameDispatchesByIndex(t, capture, plan)
	offsets := make([]int64, len(plan.inputs)+2)
	for inputIndex, dispatch := range dispatches {
		offsets[inputIndex] = dispatch.ReadyBoundaryEndByteOffset
	}
	offsets[len(plan.inputs)] = bounds.beforeOffset
	offsets[len(plan.inputs)+1] = bounds.afterOffset
	replayCheckpoints := make([]pty.ReplayCheckpoint, len(offsets))
	for index, offset := range offsets {
		replayCheckpoints[index] = pty.ReplayCheckpoint{ByteOffset: offset}
	}
	screens, err := pty.ReplayCheckpointScreens(capture, replayCheckpoints)
	if err != nil {
		t.Fatalf("replay detail checkpoints: %v", err)
	}
	screenBefore := func(inputIndex int) pty.ScreenSnapshot { return screens[inputIndex] }

	traversal := make([]pty.ScreenSnapshot, 0, len(plan.newerTraversalInputIndices))
	for _, inputIndex := range plan.newerTraversalInputIndices {
		traversal = append(traversal, screenBefore(inputIndex))
	}
	if len(traversal) < 2 {
		t.Fatalf("newer traversal checkpoints = %d, want at least 2", len(traversal))
	}

	checkpoints := detailPrecutFrameCheckpoints{
		initialNewest:      screenBefore(plan.initialNewestInputIndex),
		centerOwned:        traversal[0],
		returnedNewest:     traversal[len(traversal)-1],
		newerEdgeExhausted: screenBefore(plan.afterNewerExhaustionInputIndex),
		selectedShell:      screenBefore(plan.selectedShellNextInputIndex),
		selectedMarkdown:   screenBefore(plan.selectedMarkdownExitInputIndex),
		finalOngoing:       screenBefore(plan.finalOngoingExitInputIndex),
		ongoingBefore:      screens[len(plan.inputs)],
		ongoingAfter:       screens[len(plan.inputs)+1],
	}
	assertDetailCenterToNewestTraversal(t, checkpoints, traversal)
	return checkpoints
}

func detailPrecutFrameDispatchesByIndex(t *testing.T, capture pty.Capture, plan detailPrecutFramePlan) []*pty.FrameInputDispatch {
	t.Helper()
	if got, want := len(capture.FrameInputDispatches), len(plan.inputs); got != want {
		t.Fatalf("frame input dispatches = %d, want %d", got, want)
	}
	dispatches := make([]*pty.FrameInputDispatch, len(plan.inputs))
	for index := range capture.FrameInputDispatches {
		dispatch := capture.FrameInputDispatches[index]
		if dispatch.Phase != pty.PhaseScenarioComplete {
			t.Fatalf("frame input dispatch phase = %d, want scenario complete", dispatch.Phase)
		}
		if dispatch.InputIndex < 0 || dispatch.InputIndex >= len(dispatches) {
			t.Fatalf("frame input dispatch index = %d, inputs = %d", dispatch.InputIndex, len(dispatches))
		}
		if got, want := dispatch.ReadyBoundary, plan.inputs[dispatch.InputIndex].Readiness; got != want {
			t.Fatalf("frame input dispatch %d boundary = %d, want %d", dispatch.InputIndex, got, want)
		}
		if dispatches[dispatch.InputIndex] != nil {
			t.Fatalf("duplicate frame input dispatch index = %d", dispatch.InputIndex)
		}
		dispatchCopy := dispatch
		dispatches[dispatch.InputIndex] = &dispatchCopy
	}
	for inputIndex, dispatch := range dispatches {
		if dispatch == nil {
			t.Fatalf("frame input dispatch missing for index = %d", inputIndex)
		}
	}
	return dispatches
}

func assertDetailCenterToNewestTraversal(t *testing.T, checkpoints detailPrecutFrameCheckpoints, traversal []pty.ScreenSnapshot) {
	t.Helper()
	assertInteriorCenterDetailSelection(t, checkpoints.centerOwned)
	assertNewestDetailSelection(t, checkpoints.returnedNewest)

	previous := detailSelectionSnapshotFromScreen(t, traversal[0])
	for index := 1; index < len(traversal); index++ {
		current := detailSelectionSnapshotFromScreen(t, traversal[index])
		if detailSelectionSnapshotsEqual(previous, current) {
			t.Fatalf("newer traversal selection did not change before input index %d", index)
		}
		previous = current
	}

	initialNewest := detailSelectionSnapshotFromScreen(t, checkpoints.initialNewest)
	returnedNewest := detailSelectionSnapshotFromScreen(t, checkpoints.returnedNewest)
	if !detailSelectionSnapshotsEqual(initialNewest, returnedNewest) {
		t.Fatal("newer traversal did not return to the initial newest selection")
	}
	exhaustedNewest := detailSelectionSnapshotFromScreen(t, checkpoints.newerEdgeExhausted)
	if !detailSelectionSnapshotsEqual(returnedNewest, exhaustedNewest) {
		t.Fatal("local newer-edge exhaustion changed the newest selection")
	}
}

func assertInteriorCenterDetailSelection(t *testing.T, screen pty.ScreenSnapshot) {
	t.Helper()
	selectedRows, _ := assertContinuousSelectedLens(t, screen)
	contentRows := selectedContentRows(screen, selectedRows)
	if len(contentRows) == 0 {
		t.Fatal("interior center selection has no content rows")
	}
	if selectedRows[0] == 0 || selectedRows[len(selectedRows)-1] >= screen.Dimensions.Rows-2 {
		t.Fatalf("center-owned selection touches a viewport edge: rows=%v", selectedRows)
	}
	selectionCenter := (contentRows[0] + contentRows[len(contentRows)-1]) / 2
	viewportCenter := (screen.Dimensions.Rows - 1) / 2
	if distance := detailAbsoluteDistance(selectionCenter, viewportCenter); distance > 2 {
		t.Fatalf(
			"center-owned selection midpoint = %d, viewport center = %d, distance = %d",
			selectionCenter,
			viewportCenter,
			distance,
		)
	}
}

func detailAbsoluteDistance(left, right int) int {
	if left < right {
		return right - left
	}
	return left - right
}

func detailSelectionSnapshotFromScreen(t *testing.T, screen pty.ScreenSnapshot) detailSelectionSnapshot {
	t.Helper()
	selectedRows, _ := assertContinuousSelectedLens(t, screen)
	rows := make([]detailSelectedRowSnapshot, 0, len(selectedRows))
	for _, rowIndex := range selectedRows {
		rows = append(rows, detailSelectedRowSnapshot{
			index: rowIndex,
			cells: append([]pty.Cell(nil), screen.Cells[rowIndex]...),
		})
	}
	return detailSelectionSnapshot{rows: rows}
}

func detailSelectionSnapshotsEqual(left, right detailSelectionSnapshot) bool {
	return slices.EqualFunc(left.rows, right.rows, func(leftRow, rightRow detailSelectedRowSnapshot) bool {
		return leftRow.index == rightRow.index && slices.Equal(leftRow.cells, rightRow.cells)
	})
}

func (checkpoints detailPrecutFrameCheckpoints) proofScreens() []detailPrecutProofScreen {
	return []detailPrecutProofScreen{
		{name: "newest-non-expandable", screen: checkpoints.initialNewest},
		{name: "center-owned-selection", screen: checkpoints.centerOwned},
		{name: "newest-after-traversal", screen: checkpoints.returnedNewest},
		{name: "newer-edge-exhausted", screen: checkpoints.newerEdgeExhausted},
		{name: "selected-shell-collapsed", screen: checkpoints.selectedShell},
		{name: "selected-markdown-expanded", screen: checkpoints.selectedMarkdown},
		{name: "ongoing-before-exit", screen: checkpoints.finalOngoing},
	}
}
