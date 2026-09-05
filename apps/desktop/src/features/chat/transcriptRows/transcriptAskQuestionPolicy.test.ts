import { describe, expect, it } from "vitest";

import type { ChatTranscriptCommittedRow } from "@/api";

import {
  askQuestionCopyText,
  isAskQuestionToolRow,
  type TranscriptAskQuestionToolRow,
} from "./transcriptAskQuestionPolicy";

describe("Chat Ask Question policy", () => {
  it("claims typed Questions even when the server marks them hidden", () => {
    expect(isAskQuestionToolRow(questionRow("hidden"))).toBe(true);
  });

  it("copies freeform, selected-option, and failed Question content in display order", () => {
    expect(askQuestionCopyText(questionRow("ongoing_collapsed", false, 2, "commentary"))).toBe(
      "question\n\nsecond\n\ncommentary",
    );
    expect(askQuestionCopyText(questionRow("ongoing_collapsed", false, null, "freeform"))).toBe(
      "question\n\nfreeform",
    );
    expect(askQuestionCopyText(questionRow("ongoing_collapsed", true, null, "failed Question"))).toBe(
      "question\n\nfailed Question",
    );
  });
});

function questionRow(
  visibility: ChatTranscriptCommittedRow["Visibility"],
  isError = false,
  selectedOptionNumber: number | null = 2,
  freeform: string | null = "commentary",
): TranscriptAskQuestionToolRow {
  return {
    Visibility: visibility,
    Integrity: 0,
    Kind: "tool",
    Locator: { event_sequence: 1, row_ordinal: 1 },
    User: null,
    Assistant: null,
    Tool: {
      StepID: null,
      ToolCallID: "question-call",
      ToolName: "ask_question",
      Text: isError ? (freeform ?? "failed Question") : "answered",
      IsError: isError,
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
        PatchPresentation: null,
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
      QuestionAnswer: isError ? null : { SelectedOptionNumber: selectedOptionNumber, Freeform: freeform },
    },
    ReasoningTrace: null,
    Notice: null,
    ReviewerFeedback: null,
    ReviewerError: null,
  } as const;
}
