import { describe, expect, it } from "vitest";

import {
  creatableWorkflowNodeKinds,
  hasWorkflowNodeMetadataTooltip,
  isInspectableWorkflowNodeKind,
} from "./workflowGraphNodeKinds";

describe("workflowGraphNodeKinds", () => {
  it("keeps start, agent, join, and terminal nodes inspectable", () => {
    expect(["start", "agent", "join", "terminal"].filter(isInspectableWorkflowNodeKind)).toEqual([
      "start",
      "agent",
      "join",
      "terminal",
    ]);
    expect(isInspectableWorkflowNodeKind("unsupported")).toBe(false);
  });

  it("keeps metadata tooltip only on internal join nodes", () => {
    expect(hasWorkflowNodeMetadataTooltip("agent")).toBe(false);
    expect(hasWorkflowNodeMetadataTooltip("start")).toBe(false);
    expect(hasWorkflowNodeMetadataTooltip("join")).toBe(true);
    expect(hasWorkflowNodeMetadataTooltip("terminal")).toBe(false);
  });

  it("owns the ordered quick-add node choices", () => {
    expect(creatableWorkflowNodeKinds.map((item) => item.kind)).toEqual(["agent", "script", "terminal"]);
  });
});
