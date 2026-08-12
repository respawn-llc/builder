import { afterEach, describe, expect, it, vi } from "vitest";

import { newWorkflowTopologyID, type WorkflowTopologyIDKind } from "./workflowTopologyID";

describe("newWorkflowTopologyID", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it.each<WorkflowTopologyIDKind>(["node", "nodeGroup", "transitionGroup", "edge"])(
    "uses a bare UUID candidate for %s creation",
    (kind) => {
      const candidate = "12345678-1234-4234-9234-123456789abc";
      vi.spyOn(globalThis.crypto, "randomUUID").mockReturnValue(candidate);

      expect(newWorkflowTopologyID(kind)).toBe(candidate);
    },
  );
});
