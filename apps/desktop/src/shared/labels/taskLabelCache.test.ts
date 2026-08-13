import type { InfiniteData } from "@tanstack/react-query";
import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";

import type { BoardCard, BoardNodeCardsPage } from "@/api";
import { queryKeys } from "@/app-facade";
import {
  patchExistingTaskLabelProjections,
  pruneDeletedLabelFromExistingCaches,
  removeDeletedTaskFromExistingCaches,
} from "./taskLabelCache";

const boardCacheKey = [...queryKeys.allBoardNodeCards, "project-1", "workflow-1", "node-1"];
const alphaID = "38bf0da7-a3f7-4c15-bc5f-c8fca538e667";
const betaID = "942495c2-5958-4959-8445-94046ad74fbd";

describe("task label board-cache projections", () => {
  it("preserves numeric board offsets when projecting changed labels", () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(boardCacheKey, boardCardsData(["task-1"]));

    patchExistingTaskLabelProjections(queryClient, "task-1", ["label-new"]);

    const data = queryClient.getQueryData<InfiniteData<BoardNodeCardsPage, number>>(boardCacheKey);
    expect(data?.pageParams).toEqual([25]);
    expect(data?.pages[0]?.cards[0]?.labelIDs).toEqual(["label-new"]);
  });

  it("preserves numeric board offsets when removing a deleted task", () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(boardCacheKey, boardCardsData(["task-1", "task-2"]));
    queryClient.setQueryData(queryKeys.task("task-1"), { id: "task-1", labelIDs: [] });
    queryClient.setQueryData(queryKeys.taskLabels("task-1"), { taskID: "task-1", labelIDs: [] });

    removeDeletedTaskFromExistingCaches(queryClient, "task-1");

    const data = queryClient.getQueryData<InfiniteData<BoardNodeCardsPage, number>>(boardCacheKey);
    expect(data?.pageParams).toEqual([25]);
    expect(data?.pages[0]?.cards.map((card) => card.id)).toEqual(["task-2"]);
    expect(queryClient.getQueryData(queryKeys.task("task-1"))).toBeUndefined();
    expect(queryClient.getQueryData(queryKeys.taskLabels("task-1"))).toBeUndefined();
  });

  it("prunes a deleted Label from every retained Label projection", () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(queryKeys.projectLabels("project-1"), {
      projectID: "project-1",
      labels: [
        { id: alphaID, name: "Alpha" },
        { id: betaID, name: "Beta" },
      ],
    });
    queryClient.setQueryData(queryKeys.taskLabels("task-1"), {
      taskID: "task-1",
      labelIDs: [alphaID, betaID],
    });
    queryClient.setQueryData(queryKeys.task("task-1"), {
      id: "task-1",
      labelIDs: [alphaID, betaID],
    });
    queryClient.setQueryData(boardCacheKey, boardCardsData(["task-1"]));
    patchExistingTaskLabelProjections(queryClient, "task-1", [alphaID, betaID]);

    pruneDeletedLabelFromExistingCaches(queryClient, "project-1", alphaID);

    expect(queryClient.getQueryData(queryKeys.projectLabels("project-1"))).toEqual({
      projectID: "project-1",
      labels: [{ id: betaID, name: "Beta" }],
    });
    expect(queryClient.getQueryData(queryKeys.taskLabels("task-1"))).toEqual({
      taskID: "task-1",
      labelIDs: [betaID],
    });
    expect(queryClient.getQueryData(queryKeys.task("task-1"))).toMatchObject({
      labelIDs: [betaID],
    });
    expect(
      queryClient.getQueryData<InfiniteData<BoardNodeCardsPage, number>>(boardCacheKey)?.pages[0]?.cards[0]
        ?.labelIDs,
    ).toEqual([betaID]);
  });
});

function boardCardsData(taskIDs: readonly string[]): InfiniteData<BoardNodeCardsPage, number> {
  return {
    pageParams: [25],
    pages: [
      {
        projectID: "project-1",
        workflowID: "workflow-1",
        nodeID: "node-1",
        cards: taskIDs.map(boardCard),
        nextOffset: null,
        generatedAt: 0,
      },
    ],
  };
}

function boardCard(id: string): BoardCard {
  return {
    id,
    shortID: id,
    title: id,
    preview: { markdown: "", truncated: false },
    workflowID: "workflow-1",
    activeNodeIDs: [],
    sourceWorkspace: {
      id: "workspace-1",
      name: "Workspace",
      rootPath: "/workspace",
      availability: "available",
      isPrimary: true,
      updatedAt: 0,
    },
    status: {
      kind: "backlog",
      nativeState: "backlog",
      nodeIDs: [],
      attentionTypes: [],
    },
    actions: {
      canStart: false,
      canInterrupt: false,
      canResume: false,
      canDelete: false,
    },
    labelIDs: [],
    dependencyProgress: null,
    updatedAt: 0,
  };
}
