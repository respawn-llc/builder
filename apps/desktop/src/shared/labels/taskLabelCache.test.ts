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
import {
  patchExistingTaskLabelAssignment,
  patchExistingTaskLabelProjections,
  pruneDeletedLabelFromExistingCaches,
  removeDeletedTaskFromExistingCaches,
} from "./index";

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

  it("removes a deleted task from task and paged label-bearing caches", async () => {
    const queryClient = new QueryClient();
    const detail = await createTaskDetailFixture();
    const boardKey = queryKeys.boardNodeCards("project-1", "workflow-1", "node-1", noTaskLabelFilter);
    const listKey = [...queryKeys.allTaskLists, "project-1"];
    queryClient.setQueryData(queryKeys.task(detail.id), detail);
    queryClient.setQueryData(queryKeys.taskLabels(detail.id), {
      taskID: detail.id,
      labelIDs: [priorityID],
    });
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
    queryClient.setQueryData<TaskListPage>(listKey, taskListPage());

    removeDeletedTaskFromExistingCaches(queryClient, detail.id);

    expect(queryClient.getQueryData(queryKeys.task(detail.id))).toBeUndefined();
    expect(queryClient.getQueryData(queryKeys.taskLabels(detail.id))).toBeUndefined();
    expect(
      queryClient.getQueryData<InfiniteData<BoardNodeCardsPage, string | null>>(boardKey)?.pages[0]?.cards,
    ).toMatchObject([{ id: "task-other" }]);
    expect(queryClient.getQueryData<TaskListPage>(listKey)?.tasks).toMatchObject([{ id: "task-other" }]);
  });

  it("prunes a deleted label from assignment and every existing task projection", async () => {
    const queryClient = new QueryClient();
    const detail = {
      ...(await createTaskDetailFixture()),
      labelIDs: [priorityID],
    };
    const boardKey = queryKeys.boardNodeCards("project-1", "workflow-1", "node-1", noTaskLabelFilter);
    const listKey = [...queryKeys.allTaskLists, "project-1"];
    queryClient.setQueryData(queryKeys.task(detail.id), detail);
    queryClient.setQueryData(queryKeys.taskLabels(detail.id), {
      taskID: detail.id,
      labelIDs: [priorityID],
    });
    queryClient.setQueryData<InfiniteData<BoardNodeCardsPage, string | null>>(boardKey, {
      pages: [
        {
          projectID: "project-1",
          workflowID: "workflow-1",
          nodeID: "node-1",
          cards: [{ ...boardCard(detail.id), labelIDs: [priorityID] }],
          previousPageToken: null,
          nextPageToken: null,
          generatedAt: 1,
        },
      ],
      pageParams: [null],
    });
    const listPage = taskListPage();
    queryClient.setQueryData<TaskListPage>(listKey, {
      ...listPage,
      tasks: listPage.tasks.map((task) => ({ ...task, labelIDs: [priorityID] })),
    });

    pruneDeletedLabelFromExistingCaches(queryClient, priorityID);

    expect(queryClient.getQueryData<TaskDetail>(queryKeys.task(detail.id))?.labelIDs).toEqual([]);
    expect(queryClient.getQueryData(queryKeys.taskLabels(detail.id))).toEqual({
      taskID: detail.id,
      labelIDs: [],
    });
    expect(
      queryClient.getQueryData<InfiniteData<BoardNodeCardsPage, string | null>>(boardKey)?.pages[0]?.cards,
    ).toMatchObject([{ id: detail.id, labelIDs: [] }]);
    expect(queryClient.getQueryData<TaskListPage>(listKey)?.tasks).toMatchObject([
      { id: "task-1", labelIDs: [] },
      { id: "task-other", labelIDs: [] },
    ]);
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
