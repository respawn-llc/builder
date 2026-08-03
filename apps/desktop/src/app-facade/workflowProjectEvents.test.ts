import type { WorkflowProjectEvent } from "@/api";
import { workflowProjectEventCanChangeTaskSearch } from "./workflowProjectEvents";

describe("Workflow Project event search effects", () => {
  it("refreshes Task Search only for Task resources", () => {
    const event = {
      action: "updated",
      occurredAtUnixMs: 1,
      primaryEntityID: "task-1",
      projectID: "project-1",
      relatedIDs: [],
      resource: "task",
      workflowID: null,
    } satisfies WorkflowProjectEvent;
    expect(workflowProjectEventCanChangeTaskSearch(event)).toBe(true);
    expect(
      workflowProjectEventCanChangeTaskSearch({
        ...event,
        primaryEntityID: "workflow-1",
        resource: "workflow",
      }),
    ).toBe(false);
  });
});
