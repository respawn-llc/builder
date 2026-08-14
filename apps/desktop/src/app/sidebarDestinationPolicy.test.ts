import type { SidebarBackResult, SidebarDestination } from "@/app-facade";
import { sidebarDestinationPolicy } from "./sidebarDestinationPolicy";

describe("sidebarDestinationPolicy", () => {
  it("stages a created child in the retained New Task Draft", () => {
    const destination = {
      boardQueryWorkflowID: "workflow-1",
      kind: "newTask",
      projectID: "project-1",
      workflowID: "workflow-1",
    } satisfies SidebarDestination;
    const retainedState = {
      formValues: {
        body: "Parent body",
        sourceWorkspaceID: "workspace-1",
        title: "Parent",
      },
      preparedDependencies: [],
      selectedLabelIDs: ["label-1"],
    };
    const result = {
      kind: "newTaskCreated",
      direction: "blocked-by",
      task: {
        id: "task-child",
        shortID: "KENT-2",
        status: {
          kind: "backlog",
          nativeState: "active",
          nodeIDs: [],
          attentionTypes: [],
        },
        title: "Child",
        workflowID: "workflow-1",
      },
    } satisfies SidebarBackResult;

    expect(sidebarDestinationPolicy.applyBackResult(destination, retainedState, result)).toEqual({
      ...retainedState,
      preparedDependencies: [
        {
          direction: "blocked-by",
          taskID: "task-child",
          shortID: "KENT-2",
          status: result.task.status,
          title: "Child",
          workflowID: "workflow-1",
        },
      ],
    });
  });
});
