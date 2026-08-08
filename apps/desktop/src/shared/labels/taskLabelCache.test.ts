import type { InfiniteData } from "@tanstack/react-query";
import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";

import type { BoardCard, BoardNodeCardsPage } from "@/api";
import { queryKeys } from "@/app-facade";
import { patchExistingTaskLabelProjections, removeDeletedTaskFromExistingCaches } from "./taskLabelCache";

const boardCacheKey = [...queryKeys.allBoardNodeCards, "project-1", "workflow-1", "node-1"];

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

    removeDeletedTaskFromExistingCaches(queryClient, "task-1");

    const data = queryClient.getQueryData<InfiniteData<BoardNodeCardsPage, number>>(boardCacheKey);
    expect(data?.pageParams).toEqual([25]);
    expect(data?.pages[0]?.cards.map((card) => card.id)).toEqual(["task-2"]);
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
