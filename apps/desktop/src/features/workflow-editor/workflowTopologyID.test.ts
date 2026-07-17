import { describe, expect, it, vi } from "vitest";

import { newWorkflowTopologyID, workflowTopologyIDFromUUID } from "./workflowTopologyID";

describe("workflowTopologyID", () => {
  it("prefixes topology ids by entity kind", () => {
    expect(workflowTopologyIDFromUUID("node", "ABC")).toBe("workflow-node-abc");
    expect(workflowTopologyIDFromUUID("edge", "ABC")).toBe("workflow-edge-abc");
    expect(workflowTopologyIDFromUUID("transitionGroup", "ABC")).toBe("workflow-transition-group-abc");
    expect(workflowTopologyIDFromUUID("nodeGroup", "ABC")).toBe("workflow-node-group-abc");
  });

  it("uses browser crypto randomUUID", () => {
    const randomUUID = vi
      .spyOn(globalThis.crypto, "randomUUID")
      .mockReturnValue("00000000-0000-4000-8000-000000000001");

    expect(newWorkflowTopologyID("node")).toBe("workflow-node-00000000-0000-4000-8000-000000000001");
    expect(randomUUID).toHaveBeenCalledOnce();

    randomUUID.mockRestore();
  });
});
