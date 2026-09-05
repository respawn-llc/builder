import { describe, expect, it } from "vitest";

import { projectTaskWorkflowInitialState } from "./projectTaskSurfaceState";

describe("projectTaskWorkflowInitialState", () => {
  it("shows the initial error boundary after an established-less query fails", () => {
    expect(projectTaskWorkflowInitialState(false, true, true)).toEqual({
      failed: true,
      loading: false,
    });
  });
});
