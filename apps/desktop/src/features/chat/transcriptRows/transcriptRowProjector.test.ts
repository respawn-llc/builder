import { describe, expect, it } from "vitest";

import type { ChatTranscriptCommittedRow } from "@/api";

import { projectTranscriptRow, transcriptRowContentText } from "./transcriptRowProjector";

describe("Chat transcript row projector", () => {
  it("applies the row-family inclusion, source, and expansion policy", () => {
    const cases: readonly {
      name: string;
      row: ChatTranscriptCommittedRow;
      expected: {
        bodyKind:
          | "markdown"
          | "plain_text"
          | "structured_notice"
          | "reviewer_feedback"
          | "reviewer_error"
          | "ask_question";
        defaultExpanded: boolean;
      } | null;
    }[] = [
      {
        name: "explicitly hidden notice",
        row: noticeRow({ Visibility: "hidden" }),
        expected: null,
      },
      {
        name: "interruption feedback",
        row: noticeRow({ MessageType: "interruption" }),
        expected: null,
      },
      {
        name: "successful Reviewer status",
        row: noticeRow({ Diagnostic: { Code: "reviewer_status", Detail: "status" } }),
        expected: null,
      },
      {
        name: "typed Markdown context",
        row: noticeRow({ MessageType: "agents.md", Diagnostic: { Code: "agents.md", Detail: "context" } }),
        expected: { bodyKind: "markdown", defaultExpanded: false },
      },
      {
        name: "empty known developer context",
        row: noticeRow({ MessageType: "agents.md" }),
        expected: null,
      },
      {
        name: "runtime diagnostic",
        row: noticeRow({
          Reason: "runtime_diagnostic",
          Diagnostic: { Code: "diagnostic", Detail: "detail" },
        }),
        expected: { bodyKind: "structured_notice", defaultExpanded: false },
      },
      {
        name: "warning notice",
        row: noticeRow({ Severity: "warning", LegacyText: "warning" }),
        expected: { bodyKind: "structured_notice", defaultExpanded: true },
      },
      {
        name: "error notice",
        row: noticeRow({ Severity: "error", Diagnostic: { Code: "error_feedback", Detail: "failure" } }),
        expected: { bodyKind: "structured_notice", defaultExpanded: true },
      },
      {
        name: "Reviewer suggestions diagnostic",
        row: noticeRow({
          MessageType: "reviewer_feedback",
          Diagnostic: { Code: "reviewer_suggestions", Detail: "suggestions" },
        }),
        expected: { bodyKind: "structured_notice", defaultExpanded: false },
      },
      {
        name: "Reviewer error diagnostic",
        row: noticeRow({
          MessageType: "reviewer_feedback",
          Diagnostic: { Code: "reviewer_error", Detail: "failure" },
        }),
        expected: { bodyKind: "structured_notice", defaultExpanded: true },
      },
      {
        name: "Reviewer feedback row",
        row: reviewerFeedbackRow(),
        expected: { bodyKind: "reviewer_feedback", defaultExpanded: false },
      },
      {
        name: "Reviewer error row",
        row: reviewerErrorRow(),
        expected: { bodyKind: "reviewer_error", defaultExpanded: true },
      },
      {
        name: "answered Ask Question",
        row: questionRow(),
        expected: { bodyKind: "ask_question", defaultExpanded: true },
      },
      {
        name: "canceled Ask Question",
        row: questionRow({ isError: true, text: "canceled" }),
        expected: { bodyKind: "ask_question", defaultExpanded: false },
      },
    ];
    const markdownContextTypes = new Set([
      "skills",
      "subagents",
      "environment",
      "compaction_summary",
      "headless_mode",
      "headless_mode_exit",
      "workflow_mode",
      "active_goal_continuation",
      "agent_steer",
    ]);
    const typedContextCases = [
      "skills",
      "subagents",
      "environment",
      "compaction_summary",
      "headless_mode",
      "headless_mode_exit",
      "workflow_mode",
      "workflow_mode_exit",
      "active_goal_continuation",
      "agent_steer",
      "compaction_soon_reminder",
      "handoff_future_message",
      "manual_compaction_carryover",
      "worktree_mode",
      "worktree_mode_exit",
      "session_rebind",
      "goal",
      "background_notice",
    ].map((messageType) => ({
      name: messageType,
      row: noticeRow({ MessageType: messageType, Diagnostic: { Code: messageType, Detail: "context" } }),
      expected: {
        bodyKind: markdownContextTypes.has(messageType) ? "markdown" : "structured_notice",
        defaultExpanded: false,
      },
    }));

    for (const testCase of [...cases, ...typedContextCases]) {
      const projection = projectTranscriptRow(testCase.row, {
        reviewerFeedbackCompactText: (count) => String(count),
        structuredNoticeCompactText: (notice) => notice.Reason,
      });
      if (testCase.expected === null) {
        expect(projection, testCase.name).toBeNull();
        continue;
      }
      expect(projection, testCase.name).not.toBeNull();
      if (projection === null) {
        throw new Error(`Expected projection for ${testCase.name}.`);
      }
      expect(projection.body.kind, testCase.name).toBe(testCase.expected.bodyKind);
      expect(projection.defaultExpanded, testCase.name).toBe(testCase.expected.defaultExpanded);
      expect(projection.copySource, testCase.name).toEqual(projection.body);
      if (projection.body.kind !== "ask_question") {
        expect(
          transcriptRowContentText(projection.body, {
            structuredNoticeText: (notice) => notice.Diagnostic?.Detail ?? notice.Reason,
          }),
          testCase.name,
        ).toBe(
          transcriptRowContentText(projection.copySource, {
            structuredNoticeText: (notice) => notice.Diagnostic?.Detail ?? notice.Reason,
          }),
        );
      }
    }
  });

  it("covers typed notice facts and keeps structured body/copy source selection", () => {
    const completeCompactionDetail = "complete compaction detail";
    const cases: readonly {
      name: string;
      row: ChatTranscriptCommittedRow;
      defaultExpanded: boolean;
    }[] = [
      {
        name: "cache warning",
        row: noticeRow({
          Reason: "cache_warning",
          Severity: "warning",
          CacheWarning: {
            Scope: "conversation",
            Reason: "non_postfix",
            LostInputTokens: 10_000,
            Visibility: "ongoing",
          },
        }),
        defaultExpanded: true,
      },
      {
        name: "compaction count",
        row: noticeRow({
          Reason: "compaction",
          MessageType: "compaction_summary",
          Compaction: { Count: 2, Detail: null },
        }),
        defaultExpanded: false,
      },
      {
        name: "compaction detail",
        row: noticeRow({
          Reason: "compaction",
          MessageType: "compaction_summary",
          Compaction: { Count: 2, Detail: completeCompactionDetail },
        }),
        defaultExpanded: false,
      },
      {
        name: "tool output repair",
        row: noticeRow({
          Reason: "tool_output_repair",
          Severity: "warning",
          ToolOutputRepair: { kind: "fresh_resource", count: 2 },
        }),
        defaultExpanded: true,
      },
      {
        name: "provider model mismatch",
        row: noticeRow({
          Reason: "provider_model_mismatch",
          Severity: "warning",
          ProviderModelMismatch: { requested_model: "requested", served_model: "served" },
        }),
        defaultExpanded: true,
      },
      {
        name: "structured worktree",
        row: noticeRow({
          MessageType: "worktree_mode",
          Worktree: {
            Branch: "feature",
            WorktreePath: "/workspace/feature",
            WorkspaceRoot: "/workspace",
            EffectiveCwd: "/workspace/feature",
          },
          Diagnostic: { Code: "worktree_mode", Detail: "complete worktree detail" },
        }),
        defaultExpanded: false,
      },
      {
        name: "empty unknown developer context",
        row: noticeRow({
          Reason: "runtime_diagnostic",
          MessageType: "future_context",
          Diagnostic: null,
        }),
        defaultExpanded: true,
      },
      {
        name: "error feedback message kind",
        row: noticeRow({
          MessageType: "error_feedback",
          Diagnostic: { Code: "error_feedback", Detail: "failure" },
        }),
        defaultExpanded: true,
      },
      {
        name: "compaction reminder message kind",
        row: noticeRow({
          MessageType: "compaction_soon_reminder",
          Diagnostic: { Code: "compaction_soon_reminder", Detail: "soon" },
        }),
        defaultExpanded: false,
      },
    ];
    const labels = {
      reviewerFeedbackCompactText: (count: number) => String(count),
      structuredNoticeCompactText: () => "localized typed notice",
    };

    for (const testCase of cases) {
      const projection = projectTranscriptRow(testCase.row, labels);
      expect(projection, testCase.name).not.toBeNull();
      if (projection === null) {
        throw new Error(`Expected projection for ${testCase.name}.`);
      }
      expect(projection.body.kind, testCase.name).toBe("structured_notice");
      expect(projection.defaultExpanded, testCase.name).toBe(testCase.defaultExpanded);
      expect(projection.compactText, testCase.name).not.toBe(testCase.row.Notice?.Reason);
      expect(projection.copySource, testCase.name).toBe(projection.body);
    }

    const compactionDetailCase = cases.find((testCase) => testCase.name === "compaction detail");
    if (compactionDetailCase === undefined) {
      throw new Error("Compaction detail case is missing.");
    }
    const compactionProjection = projectTranscriptRow(compactionDetailCase.row, labels);
    if (compactionProjection === null) {
      throw new Error("Expected compaction projection.");
    }
    const formatter = {
      structuredNoticeText: (notice: Notice) => notice.Compaction?.Detail ?? "",
    };
    expect(transcriptRowContentText(compactionProjection.body, formatter)).toBe(completeCompactionDetail);
    expect(transcriptRowContentText(compactionProjection.copySource, formatter)).toBe(
      completeCompactionDetail,
    );
  });
});

type Notice = NonNullable<ChatTranscriptCommittedRow["Notice"]>;

function noticeRow(input: Partial<Notice> & { Visibility?: ChatTranscriptCommittedRow["Visibility"] } = {}) {
  const { Visibility = "ongoing", ...noticeInput } = input;
  return {
    Visibility,
    Integrity: 0,
    Kind: "notice",
    Locator: { event_sequence: 1, row_ordinal: 1 },
    User: null,
    Assistant: null,
    Tool: null,
    ReasoningTrace: null,
    Notice: {
      Reason: "legacy_untyped_notice",
      Severity: "info",
      ...noticeInput,
    },
    ReviewerFeedback: null,
    ReviewerError: null,
  } satisfies ChatTranscriptCommittedRow;
}

function questionRow(input: { isError?: boolean; text?: string } = {}) {
  return {
    Visibility: "ongoing_collapsed",
    Integrity: 0,
    Kind: "tool",
    Locator: { event_sequence: 1, row_ordinal: 1 },
    User: null,
    Assistant: null,
    Tool: {
      StepID: null,
      ToolCallID: "question-call",
      ToolName: "ask_question",
      Text: input.text ?? "answered",
      IsError: input.isError ?? false,
      ResultSummary: null,
      CondensedText: null,
      Presentation: {
        ToolName: "ask_question",
        Presentation: "ask_question",
        RenderBehavior: "ask_question",
        IsShell: false,
        UserInitiated: false,
        Command: "",
        CompactText: "question",
        InlineMeta: "",
        TimeoutLabel: "",
        PatchSummary: "",
        PatchDetail: "",
        PatchRender: null,
        RenderHint: null,
        Question: "question",
        Suggestions: ["first", "second"],
        RecommendedOptionIndex: 2,
        OmitSuccessfulResult: false,
        RawOutputRequested: false,
        OutputTruncated: false,
        MovedToBackground: false,
        ShellExitCode: null,
      },
      QuestionAnswer: input.isError ? null : { SelectedOptionNumber: 2, Freeform: "commentary" },
    },
    ReasoningTrace: null,
    Notice: null,
    ReviewerFeedback: null,
    ReviewerError: null,
  } satisfies ChatTranscriptCommittedRow;
}

function reviewerFeedbackRow() {
  return {
    Visibility: "ongoing_collapsed",
    Integrity: 0,
    Kind: "reviewer_feedback",
    Locator: { event_sequence: 1, row_ordinal: 1 },
    User: null,
    Assistant: null,
    Tool: null,
    ReasoningTrace: null,
    Notice: null,
    ReviewerFeedback: {
      ID: "feedback",
      StepID: "step",
      Suggestions: ["first", "second"],
      SuggestionCount: 2,
    },
    ReviewerError: null,
  } satisfies ChatTranscriptCommittedRow;
}

function reviewerErrorRow() {
  return {
    Visibility: "ongoing",
    Integrity: 0,
    Kind: "reviewer_error",
    Locator: { event_sequence: 1, row_ordinal: 1 },
    User: null,
    Assistant: null,
    Tool: null,
    ReasoningTrace: null,
    Notice: null,
    ReviewerFeedback: null,
    ReviewerError: { ID: "error", StepID: "step", Detail: "failure" },
  } satisfies ChatTranscriptCommittedRow;
}
