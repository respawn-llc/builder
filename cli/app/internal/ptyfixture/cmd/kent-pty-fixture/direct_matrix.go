package main

import (
	"encoding/json"
	"fmt"
	"os"

	"core/cli/tui/ongoing"
	"core/internal/testharness/pty"
	"core/shared/clientui"
	patchformat "core/shared/transcript/patchformat"
)

type directFixtureScript struct {
	DirectMatrix bool `json:"direct_matrix"`
}

func scriptRequestsDirectMatrix(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read script: %w", err)
	}
	var script directFixtureScript
	if err := json.Unmarshal(data, &script); err != nil {
		return false, fmt.Errorf("decode script: %w", err)
	}
	return script.DirectMatrix, nil
}

func runDirectMatrixFixture(observationPath string) error {
	if err := emitPhase(pty.PhaseMarker{Sequence: 1, Phase: pty.PhaseScenarioStart}); err != nil {
		return err
	}
	surface := ongoing.NewSurface(os.Stdout)
	_, err := surface.ApplyTerminalMessage(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageHydration,
		Hydration: &clientui.TranscriptHydration{CommittedRows: []clientui.TranscriptCommittedRow{
			userRow("VISIBILITY_O_USER"),
			assistantRow("VISIBILITY_O_MODEL"),
			noticeRow(clientui.TranscriptNoticeInfo, "VISIBILITY_OC_NOTICE toggle fast on"),
			noticeRow(clientui.TranscriptNoticeWarning, "VISIBILITY_OC_WARNING toggle supervisor off"),
			noticeRow(clientui.TranscriptNoticeError, "VISIBILITY_O_ERROR"),
			toolRow("patch", "PATCH_TOOL", patchMeta("patch", "cli/tui/model.go", 2, 1), false),
			toolRow("edit", "EDIT_TOOL", patchMeta("edit", "cli/tui/ongoing/surface.go", 1, 1), false),
			toolRow("view_image", "VIEW_IMAGE_TOOL", toolMeta("view_image", clientui.ToolPresentationDefault, "VIEW_IMAGE_TOOL"), false),
			toolRow("web_search", "WEB_SEARCH_TOOL", toolMeta("web_search", clientui.ToolPresentationDefault, "WEB_SEARCH_TOOL"), false),
			toolRow("custom_tool", "CUSTOM_TOOL", toolMeta("custom_tool", clientui.ToolPresentationDefault, "CUSTOM_TOOL"), false),
			toolRow("workflow_completion", "WORKFLOW_COMPLETION_TOOL", toolMeta("workflow_completion", clientui.ToolPresentationDefault, "WORKFLOW_COMPLETION_TOOL"), false),
			toolRow("exec_command", "SHELL_TOOL", toolMeta("exec_command", clientui.ToolPresentationShell, "SHELL_TOOL"), false),
			toolRow("ask_question", "ASK_QUESTION_TOOL", askMeta("ASK_QUESTION_TOOL"), false),
			toolRow("trigger_handoff", "TRIGGER_HANDOFF_TOOL", toolMeta("trigger_handoff", clientui.ToolPresentationDefault, "TRIGGER_HANDOFF_TOOL"), false),
			noticeRow(clientui.TranscriptNoticeInfo, "VISIBILITY_D_DETAIL_ONLY"),
			noticeRow(clientui.TranscriptNoticeInfo, "VISIBILITY_X_HIDDEN"),
		}},
	}, matrixFrame())
	if err != nil {
		return err
	}
	if _, err := surface.Render(matrixFrame()); err != nil {
		return err
	}
	if err := emitPhase(pty.PhaseMarker{Sequence: 2, Phase: pty.PhaseScenarioComplete}); err != nil {
		return err
	}
	return os.WriteFile(observationPath, []byte(`{"model_request_count":1,"final_response_consumed":true}`), 0o644)
}

func emitPhase(marker pty.PhaseMarker) error {
	encoded, err := pty.EncodePhaseMarker(marker)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(encoded)
	return err
}

func matrixFrame() ongoing.FrameInput {
	return ongoing.FrameInput{
		Size: ongoing.Size{Width: 80, Height: 24},
		Sections: []ongoing.FrameSection{
			{Kind: ongoing.FrameSectionRunState, Lines: []string{"streaming live area"}},
			{Kind: ongoing.FrameSectionInput, Lines: []string{"/status typed slash command"}},
			{Kind: ongoing.FrameSectionStatus, Lines: []string{"statusline ready"}},
		},
		Cursor: ongoing.Cursor{Visible: true, Row: 24, Column: 1},
	}
}

func userRow(text string) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: text}}
}

func assistantRow(text string) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{Text: text, Phase: clientui.MessagePhaseFinal}}
}

func noticeRow(severity clientui.TranscriptNoticeSeverity, text string) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Severity: severity, Data: clientui.TranscriptNoticeData{LegacyText: &text}}}
}

func toolRow(name string, text string, meta *clientui.ToolCallMeta, isError bool) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowTool,
		Tool: &clientui.TranscriptToolRow{ToolName: name, Text: text, CondensedText: text, IsError: isError, ToolPresentation: meta},
	}
}

func toolMeta(name string, presentation clientui.ToolPresentationKind, command string) *clientui.ToolCallMeta {
	return &clientui.ToolCallMeta{ToolName: name, Presentation: presentation, Command: command, CompactText: command}
}

func askMeta(question string) *clientui.ToolCallMeta {
	return &clientui.ToolCallMeta{ToolName: "ask_question", Presentation: clientui.ToolPresentationAskQuestion, Question: question, CompactText: question}
}

func patchMeta(name string, path string, added int, removed int) *clientui.ToolCallMeta {
	return &clientui.ToolCallMeta{
		ToolName:    name,
		CompactText: path,
		PatchRender: &patchformat.RenderedPatch{
			Files:        []patchformat.RenderedFile{{RelPath: path, Added: added, Removed: removed}},
			SummaryLines: []patchformat.RenderedLine{{Kind: patchformat.RenderedLineKindFile, Text: path, FileIndex: 0}},
		},
	}
}
