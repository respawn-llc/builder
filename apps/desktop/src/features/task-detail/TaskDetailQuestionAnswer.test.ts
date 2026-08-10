import { describe, expect, it } from "vitest";

import { questionAnswerBatchInput } from "./TaskDetailQuestionAnswer";

describe("questionAnswerBatchInput", () => {
  it("encodes whitespace-only optional text as absent while preserving commentary", () => {
    expect(
      questionAnswerBatchInput({
        freeformAnswer: " \n ",
        kind: "ordinary",
        promptID: "prompt-1",
        selectedOptionNumber: 1,
        sessionID: "session-1",
        stepID: "step-1",
      }).entries[0],
    ).toMatchObject({ freeform: null });
    expect(
      questionAnswerBatchInput({
        commentary: " keep this ",
        decision: "allow_once",
        kind: "approval",
        promptID: "prompt-2",
        sessionID: "session-1",
        stepID: "step-1",
      }).entries[0],
    ).toMatchObject({ commentary: " keep this " });
  });
});
