import { describe, expect, it } from "vitest";

import {
  priorNodeOutputPlaceholderLabel,
  workflowPromptTemplatePlaceholders,
} from "./workflowPromptTemplatePlaceholders";

describe("workflowPromptTemplatePlaceholders", () => {
  it("advertises prior Node outputs as an authorable prompt reference", () => {
    const placeholder = workflowPromptTemplatePlaceholders([]).find(
      (candidate) => candidate.kind === "info" && candidate.topic === "node_output",
    );

    expect(placeholder).toEqual({
      kind: "info",
      label: priorNodeOutputPlaceholderLabel,
      tone: "primary",
      topic: "node_output",
    });
  });
});
