import { QueryClient, type InfiniteData } from "@tanstack/react-query";

import {
  noTaskLabelFilter,
  type BoardCard,
  type BoardNodeCardsPage,
  type TaskDetail,
  type TaskListPage,
} from "@/api";
import { queryKeys } from "@/app-facade";
import { createTaskDetailFixture } from "@/test-support/task-detail";
import { patchExistingTaskLabelAssignment, patchExistingTaskLabelProjections } from "./index";

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";

describe("task label cache helpers", () => {
  it("patches an existing task detail without creating an absent task query", async () => {
    const queryClient = new QueryClient();
    const detail = await createTaskDetailFixture();
    queryClient.setQueryData(queryKeys.task(detail.id), detail);
    const boardKey = queryKeys.boardNodeCards("project-1", "workflow-1", "node-1", noTaskLabelFilter);
    queryClient.setQueryData<InfiniteData<BoardNodeCardsPage, string | null>>(boardKey, {
      pages: [
        {
          projectID: "project-1",
          workflowID: "workflow-1",
          nodeID: "node-1",
          cards: [boardCard(detail.id), boardCard("task-other")],
          previousPageToken: null,
          nextPageToken: null,
          generatedAt: 1,
        },
      ],
      pageParams: [null],
    });

    patchExistingTaskLabelProjections(queryClient, detail.id, [priorityID]);

    expect(queryClient.getQueryData<TaskDetail>(queryKeys.task(detail.id))?.labelIDs).toEqual([priorityID]);
    expect(
      queryClient.getQueryData<InfiniteData<BoardNodeCardsPage, string | null>>(boardKey)?.pages[0]?.cards,
    ).toMatchObject([
      { id: detail.id, labelIDs: [priorityID] },
      { id: "task-other", labelIDs: [] },
    ]);
    expect(queryClient.getQueryData(queryKeys.task("task-absent"))).toBeUndefined();
  });

  it("patches existing generic task-list items without creating list data", () => {
    const queryClient = new QueryClient();
    const listKey = [...queryKeys.allTaskLists, "project-1"];
    queryClient.setQueryData<TaskListPage>(listKey, taskListPage());

    patchExistingTaskLabelProjections(queryClient, "task-1", [priorityID]);

    expect(queryClient.getQueryData<TaskListPage>(listKey)?.tasks).toMatchObject([
      { id: "task-1", labelIDs: [priorityID] },
      { id: "task-other", labelIDs: [] },
    ]);
    expect(queryClient.getQueryData([...queryKeys.allTaskLists, "absent"])).toBeUndefined();
  });

  it("patches only an already-cached authoritative assignment", () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(queryKeys.taskLabels("task-1"), {
      taskID: "task-1",
      labelIDs: [],
    });

    patchExistingTaskLabelAssignment(queryClient, {
      taskID: "task-1",
      labelIDs: [priorityID],
    });
    patchExistingTaskLabelAssignment(queryClient, {
      taskID: "task-absent",
      labelIDs: [priorityID],
    });

    expect(queryClient.getQueryData(queryKeys.taskLabels("task-1"))).toEqual({
      taskID: "task-1",
      labelIDs: [priorityID],
    });
    expect(queryClient.getQueryData(queryKeys.taskLabels("task-absent"))).toBeUndefined();
  });
});

function boardCard(taskID: string): BoardCard {
  return {
    id: taskID,
    shortID: taskID,
    title: taskID,
    preview: { markdown: "", truncated: false },
    workflowID: "workflow-1",
    activeNodeIDs: ["node-1"],
    sourceWorkspace: {
      id: "workspace-1",
      name: "Workspace",
      rootPath: "/workspace",
      availability: "available",
      isPrimary: true,
      updatedAt: 1,
    },
    status: {
      kind: "active",
      nativeState: "active",
      nodeIDs: ["node-1"],
      runIDs: [],
      attentionTypes: [],
    },
    actions: {
      canStart: false,
      canInterrupt: false,
      canResume: false,
      canCancel: true,
      manualMoveTargetNodeIDs: [],
    },
    labelIDs: [],
    updatedAt: 1,
  };
}

function taskListPage(): TaskListPage {
  return {
    scope: {
      projectID: "project-1",
      workflowID: "workflow-1",
    },
    matchingWorkflowCardinality: "one",
    nextPageToken: null,
    generatedAt: 1,
    tasks: ["task-1", "task-other"].map((taskID) => ({
      id: taskID,
      shortID: taskID,
      workflowID: "workflow-1",
      workflowName: "Workflow",
      title: taskID,
      createdAt: 1,
      updatedAt: 1,
      columnKeys: ["node-1"],
      status: {
        kind: "active",
        nativeState: "active",
        nodeIDs: ["node-1"],
        runIDs: [],
        attentionTypes: [],
      },
      runCount: 0,
      labelIDs: [],
    })),
  };
}
