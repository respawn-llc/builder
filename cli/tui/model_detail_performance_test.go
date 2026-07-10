package tui

import (
	"fmt"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDetailNavigationAllocationsDoNotScaleWithAppliedPageSize(t *testing.T) {
	small := detailPerformanceModel(t, 4)
	large := detailPerformanceModel(t, 84)

	smallAllocs := testing.AllocsPerRun(20, func() {
		navigateDetailPerformanceModel(small)
	})
	largeAllocs := testing.AllocsPerRun(20, func() {
		navigateDetailPerformanceModel(large)
	})

	const fixedAllocationTolerance = 32
	allowed := smallAllocs*1.5 + fixedAllocationTolerance
	if largeAllocs > allowed {
		t.Fatalf(
			"large-page navigation allocations = %.1f, want <= %.1f (small page %.1f); navigation cost scales with applied page membership",
			largeAllocs,
			allowed,
			smallAllocs,
		)
	}
}

func TestDetailViewAllocationsDoNotScaleWithAppliedPageSize(t *testing.T) {
	small := detailPerformanceModel(t, 4)
	large := detailPerformanceModel(t, 84)

	smallAllocs := testing.AllocsPerRun(20, func() {
		_ = small.View()
	})
	largeAllocs := testing.AllocsPerRun(20, func() {
		_ = large.View()
	})

	const fixedAllocationTolerance = 32
	allowed := smallAllocs*1.5 + fixedAllocationTolerance
	if largeAllocs > allowed {
		t.Fatalf(
			"large-page View allocations = %.1f, want <= %.1f (small page %.1f); render cost scales with applied page membership",
			largeAllocs,
			allowed,
			smallAllocs,
		)
	}
}

func TestDetailNavigationRuntimeDoesNotScaleWithAppliedPageSize(t *testing.T) {
	small := detailPerformanceModel(t, 4)
	large := detailPerformanceModel(t, 84)

	smallResult := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			navigateDetailPerformanceModel(small)
		}
	})
	largeResult := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			navigateDetailPerformanceModel(large)
		}
	})

	const (
		runtimeRatioTolerance = int64(2)
		fixedRuntimeTolerance = int64(50_000)
	)
	allowed := smallResult.NsPerOp()*runtimeRatioTolerance + fixedRuntimeTolerance
	if largeResult.NsPerOp() > allowed {
		t.Fatalf(
			"large-page navigation runtime = %d ns/op, want <= %d ns/op (small page %d ns/op); navigation cost scales with applied page membership",
			largeResult.NsPerOp(),
			allowed,
			smallResult.NsPerOp(),
		)
	}
}

func TestDetailViewRuntimeDoesNotScaleWithAppliedPageSize(t *testing.T) {
	small := detailPerformanceModel(t, 4)
	large := detailPerformanceModel(t, 84)

	smallResult := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			_ = small.View()
		}
	})
	largeResult := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			_ = large.View()
		}
	})

	const (
		runtimeRatioTolerance = int64(2)
		fixedRuntimeTolerance = int64(100_000)
	)
	allowed := smallResult.NsPerOp()*runtimeRatioTolerance + fixedRuntimeTolerance
	if largeResult.NsPerOp() > allowed {
		t.Fatalf(
			"large-page View runtime = %d ns/op, want <= %d ns/op (small page %d ns/op); render cost scales with applied page membership",
			largeResult.NsPerOp(),
			allowed,
			smallResult.NsPerOp(),
		)
	}
}

func TestSameWidthViewportUpdatesRetainCompiledDetailProjection(t *testing.T) {
	small := detailPerformanceModel(t, 4)
	large := detailPerformanceModel(t, 84)

	smallAllocs := testing.AllocsPerRun(20, func() {
		_, _ = small.Update(SetViewportSizeMsg{Lines: 12, Width: small.viewportWidth})
	})
	largeAllocs := testing.AllocsPerRun(20, func() {
		_, _ = large.Update(SetViewportSizeMsg{Lines: 12, Width: large.viewportWidth})
	})

	const fixedAllocationTolerance = 16
	allowed := smallAllocs*1.5 + fixedAllocationTolerance
	if largeAllocs > allowed {
		t.Fatalf(
			"large-page same-width viewport update allocations = %.1f, want <= %.1f (small page %.1f); height-only updates rebuilt compiled detail state",
			largeAllocs,
			allowed,
			smallAllocs,
		)
	}
}

func BenchmarkDetailNavigation(b *testing.B) {
	model := detailPerformanceBenchmarkModel(b, 84)
	for b.Loop() {
		navigateDetailPerformanceModel(model)
	}
}

func BenchmarkDetailView(b *testing.B) {
	model := detailPerformanceBenchmarkModel(b, 84)
	for b.Loop() {
		_ = model.View()
	}
}

func BenchmarkDetailPageCompilation(b *testing.B) {
	rows := detailPerformanceRows(84)
	for b.Loop() {
		model := NewModel()
		model.viewportWidth = 96
		model.viewportLines = 8
		model.applyDetailTranscriptPage(
			clientui.TranscriptPage{Entries: rows},
			DetailTranscriptAnchorBottom,
			0,
			nil,
		)
	}
}

func BenchmarkDetailWidthReflow(b *testing.B) {
	model := detailPerformanceBenchmarkModel(b, 84)
	for b.Loop() {
		reflowed := model
		reflowed.viewportWidth = 72
		reflowed.reflowDetailProjection()
	}
}

func navigateDetailPerformanceModel(seed Model) {
	next, _ := seed.Update(tea.KeyMsg{Type: tea.KeyUp})
	current := next.(Model)
	_, _ = current.Update(tea.KeyMsg{Type: tea.KeyDown})
}

func detailPerformanceBenchmarkModel(b *testing.B, prefixCount int) Model {
	b.Helper()
	model := NewModel()
	model.viewportWidth = 96
	model.viewportLines = 8
	model.applyDetailTranscriptPage(
		clientui.TranscriptPage{Entries: detailPerformanceRows(prefixCount)},
		DetailTranscriptAnchorBottom,
		0,
		nil,
	)
	model.mode = ModeDetail
	return model
}

func detailPerformanceModel(t *testing.T, prefixCount int) Model {
	t.Helper()
	model := NewModel()
	next, _ := model.Update(SetViewportSizeMsg{Lines: 8, Width: 96})
	model = next.(Model)
	next, _ = model.Update(SetDetailTranscriptPageMsg{
		Page: clientui.TranscriptPage{
			Entries: detailPerformanceRows(prefixCount),
		},
		Anchor: DetailTranscriptAnchorBottom,
	})
	model = next.(Model)
	next, _ = model.Update(SetModeMsg{Mode: ModeDetail})
	return next.(Model)
}

func detailPerformanceRows(prefixCount int) []clientui.TranscriptCommittedRow {
	rows := make([]clientui.TranscriptCommittedRow, 0, prefixCount+12)
	for index := 0; index < prefixCount; index++ {
		rows = append(rows, detailPerformancePrefixRow(index))
	}
	return append(rows, detailPerformanceVisibleTail()...)
}

func detailPerformancePrefixRow(index int) clientui.TranscriptCommittedRow {
	switch index % 4 {
	case 0:
		return detailUser(fmt.Sprintf(
			"### Historical request %d\n\n- preserve **semantic** Markdown\n- keep `detail` movement bounded",
			index,
		))
	case 1:
		return detailAssistant(strings.Repeat(
			fmt.Sprintf("Compiled assistant paragraph %d with wrapping pressure. ", index),
			3,
		))
	case 2:
		return detailTool(clientui.TranscriptToolRow{
			ToolCallID: fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1),
			ToolName:   "exec_command",
			Text:       "go test ./cli/tui ./cli/tui/transcriptrender -count=1",
			ToolPresentation: &clientui.ToolCallMeta{
				ToolName:     "exec_command",
				Presentation: clientui.ToolPresentationShell,
				Command:      "go test ./cli/tui ./cli/tui/transcriptrender -count=1",
				RenderHint: &clientui.ToolRenderHint{
					Kind:         clientui.ToolRenderKindShell,
					ShellDialect: clientui.ToolShellDialectPosix,
				},
			},
		})
	default:
		rendered := patchformat.Render(
			fmt.Sprintf(
				"*** Begin Patch\n*** Update File: cli/tui/perf_%d.go\n@@\n-oldValue := %d\n+newValue := %d\n*** End Patch\n",
				index,
				index,
				index+1,
			),
			"/workspace",
		)
		return detailTool(clientui.TranscriptToolRow{
			ToolCallID: fmt.Sprintf("10000000-0000-4000-8000-%012d", index+1),
			ToolName:   "patch",
			Text:       rendered.DetailText(),
			ToolPresentation: &clientui.ToolCallMeta{
				ToolName:    "patch",
				PatchRender: &rendered,
				RenderHint:  &clientui.ToolRenderHint{Kind: clientui.ToolRenderKindDiff},
			},
		})
	}
}

func detailPerformanceVisibleTail() []clientui.TranscriptCommittedRow {
	return []clientui.TranscriptCommittedRow{
		detailUser("tail user one"),
		detailUser("tail user two"),
		detailAssistant("tail assistant one"),
		detailAssistant("tail assistant two"),
		detailTool(clientui.TranscriptToolRow{
			ToolCallID: "20000000-0000-4000-8000-000000000001",
			ToolName:   "exec_command",
			Text:       "git status --short",
			ToolPresentation: &clientui.ToolCallMeta{
				ToolName:     "exec_command",
				Presentation: clientui.ToolPresentationShell,
				Command:      "git status --short",
				RenderHint: &clientui.ToolRenderHint{
					Kind:         clientui.ToolRenderKindShell,
					ShellDialect: clientui.ToolShellDialectPosix,
				},
			},
		}),
		detailTool(clientui.TranscriptToolRow{
			ToolCallID: "20000000-0000-4000-8000-000000000002",
			ToolName:   "custom_tool",
			Text:       "tail custom tool",
		}),
		detailNotice(clientui.TranscriptNoticeRow{
			Reason:     clientui.TranscriptNoticeRuntimeDiagnostic,
			Severity:   clientui.TranscriptNoticeInfo,
			Diagnostic: &clientui.TranscriptDiagnosticData{Detail: "tail diagnostic one"},
		}),
		detailNotice(clientui.TranscriptNoticeRow{
			Reason:     clientui.TranscriptNoticeRuntimeDiagnostic,
			Severity:   clientui.TranscriptNoticeInfo,
			Diagnostic: &clientui.TranscriptDiagnosticData{Detail: "tail diagnostic two"},
		}),
		{
			Visibility: clientui.EntryVisibilityOngoing,
			Integrity:  transcript.RowIntegrityValid,
			Kind:       clientui.TranscriptRowAssistant,
			Assistant:  &clientui.TranscriptAssistantRow{Text: "tail assistant three"},
		},
		detailAssistant("tail assistant four"),
		detailUser("tail user three"),
		detailUser("tail user four"),
	}
}
