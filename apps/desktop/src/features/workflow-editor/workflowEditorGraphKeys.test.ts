import { describe, expect, it } from "vitest";

import {
  isWorkflowModelKeyValid,
  uniqueWorkflowModelKey,
  workflowModelKeyFromLabel,
} from "./workflowEditorGraphKeys";

describe("workflowEditorGraphKeys", () => {
  it("normalizes labels into workflow model keys", () => {
    expect(workflowModelKeyFromLabel("Code Review!", "agent")).toBe("code_review");
    expect(workflowModelKeyFromLabel("123 Done", "agent")).toBe("x_123_done");
    expect(workflowModelKeyFromLabel(" ", "Agent Node")).toBe("agent_node");
  });

  it("creates unique suffixed keys", () => {
    expect(uniqueWorkflowModelKey("Review", ["review", "review_2"])).toBe("review_3");
  });

  it("validates server-compatible workflow model keys", () => {
    expect(isWorkflowModelKeyValid("summary_2")).toBe(true);
    expect(isWorkflowModelKeyValid("Summary")).toBe(false);
    expect(isWorkflowModelKeyValid("2_summary")).toBe(false);
    expect(isWorkflowModelKeyValid("summary-field")).toBe(false);
    expect(isWorkflowModelKeyValid("")).toBe(false);
  });
});
