import { describe, expect, it } from "vitest";

import { committedRowSchema } from "./chatSchemas";

describe("committed Ask Question rows", () => {
  it("accepts typed answers and the zero-based recommendation sentinel", () => {
    for (const recommendedOptionIndex of [0, 1, 2]) {
      expect(
        committedRowSchema.safeParse(
          askQuestionRow({
            recommendedOptionIndex,
            questionAnswer: { SelectedOptionNumber: 2, Freeform: "keep the split" },
          }),
        ).success,
      ).toBe(true);
    }
  });

  it("rejects a successful Question without typed answer facts", () => {
    expect(committedRowSchema.safeParse(askQuestionRow({ questionAnswer: null })).success).toBe(false);
  });

  it("rejects recommendations outside the offered option range", () => {
    for (const input of [
      { recommendedOptionIndex: -1, suggestions: ["first", "second"] },
      { recommendedOptionIndex: 3, suggestions: ["first", "second"] },
      { recommendedOptionIndex: 1, suggestions: [] },
    ]) {
      expect(committedRowSchema.safeParse(askQuestionRow(input)).success).toBe(false);
    }
  });

  it("requires nonblank failure detail for failed Questions", () => {
    for (const text of ["canceled", " canceled "]) {
      expect(committedRowSchema.safeParse(askQuestionRow({ isError: true, text })).success).toBe(true);
    }
    for (const text of ["", " \t\n"]) {
      expect(committedRowSchema.safeParse(askQuestionRow({ isError: true, text })).success).toBe(false);
    }
  });

  it("leaves producer-owned answer shape and consistency semantics untouched", () => {
    expect(
      committedRowSchema.safeParse(
        askQuestionRow({
          questionAnswer: { SelectedOptionNumber: null, Freeform: null },
        }),
      ).success,
    ).toBe(true);
  });
});

function askQuestionRow(input: {
  isError?: boolean;
  text?: string;
  suggestions?: string[];
  recommendedOptionIndex?: number;
  questionAnswer?: { SelectedOptionNumber: number | null; Freeform: string | null } | null;
}) {
  return {
    Visibility: "ongoing_collapsed",
    Integrity: 0,
    Kind: "tool",
    Locator: { event_sequence: 1, row_ordinal: 1 },
    User: null,
    Assistant: null,
    Tool: {
      StepID: null,
      ToolCallID: "call-question",
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
        CompactText: "Which option?",
        InlineMeta: "",
        TimeoutLabel: "",
        PatchPresentation: null,
        RenderHint: null,
        Question: "Which option?",
        Suggestions: input.suggestions ?? ["first", "second"],
        RecommendedOptionIndex: input.recommendedOptionIndex ?? 0,
        OmitSuccessfulResult: false,
        RawOutputRequested: false,
        OutputTruncated: false,
        MovedToBackground: false,
        ShellExitCode: null,
      },
      QuestionAnswer:
        input.questionAnswer === undefined
          ? {
              SelectedOptionNumber: 2,
              Freeform: "keep the split",
            }
          : input.questionAnswer,
    },
    ReasoningTrace: null,
    Notice: null,
    ReviewerFeedback: null,
    ReviewerError: null,
  };
}
