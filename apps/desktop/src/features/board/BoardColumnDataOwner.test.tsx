import { render, waitFor } from "@testing-library/react";
import { vi } from "vitest";

import type { BoardNodeCardsPage, SelectedWorkflowBoard } from "@/api";
import { BoardColumnDataOwner, type BoardColumnDataView } from "./BoardColumnDataOwner";

const boardMocks = vi.hoisted(() => ({
  cardsQuery: {
    data: { pages: new Array<BoardNodeCardsPage>() },
    error: null,
    fetchNextPage: vi.fn(),
    fetchPreviousPage: vi.fn(),
    hasNextPage: true,
    hasPreviousPage: true,
    isError: false,
    isFetchNextPageError: false,
    isFetchPreviousPageError: false,
    isFetching: true,
    isFetchingNextPage: false,
    isFetchingPreviousPage: false,
    isPending: false,
    isPlaceholderData: true,
    refetch: vi.fn(),
  },
  generation: {
    snapshot: {
      active: {
        filter: { kind: "none" as const },
        generation: 2,
        retiring: false,
        sort: { field: "title" as const, direction: "asc" as const },
      },
      desiredFilter: null,
      desiredSort: { field: "labels" as const, direction: "desc" as const },
    },
  },
}));

vi.mock("./BoardFilterGenerationRuntime", () => ({
  useBoardFilterGeneration: () => boardMocks.generation,
}));

vi.mock("./useBoardData", () => ({
  useBoardNodeCards: () => boardMocks.cardsQuery,
}));

vi.mock("@/shared/labels", () => ({
  selectOrderedProjectLabels: (
    catalog: readonly Readonly<{ id: string; name: string }>[] | undefined,
    labelIDs: readonly string[],
  ) => catalog?.filter((label) => labelIDs.includes(label.id)) ?? [],
  useProjectLabelCatalog: () => ({ data: { labels: [] } }),
}));

describe("BoardColumnDataOwner", () => {
  it("retains visible cards while a replacement sort is pending and disables pagination", async () => {
    boardMocks.cardsQuery.data = { pages: [cardsPage()] };
    const onDataViewChange = vi.fn<(view: BoardColumnDataView) => void>();

    render(
      <BoardColumnDataOwner
        board={board()}
        column={column()}
        onCardsLoadError={vi.fn()}
        onDataViewChange={onDataViewChange}
        onDataViewRelease={vi.fn()}
        onReportColumnSnapshot={vi.fn()}
      />,
    );

    await waitFor(() => {
      expect(onDataViewChange).toHaveBeenCalled();
    });
    const latestView = onDataViewChange.mock.calls.at(-1)?.[0];
    if (latestView === undefined) {
      throw new Error("Board column did not emit a data view.");
    }
    expect(latestView.cards).toEqual([
      expect.objectContaining({
        id: "task-1",
        labels: [],
      }),
    ]);
    expect(latestView.hasNextPage).toBe(false);
    expect(latestView.hasPreviousPage).toBe(false);
    expect(latestView.isFetchingNextPage).toBe(false);
    expect(latestView.isFetchingPreviousPage).toBe(false);
  });
});

function board(): SelectedWorkflowBoard {
  return {
    attachedWorkspaceCount: 1,
    columns: [column()],
    defaultWorkspaceID: "workspace-1",
    generatedAt: 1,
    groups: [],
    projectID: "project-1",
    projectKey: "PRO",
    projectName: "Project",
    selectedWorkflow: {
      description: "",
      id: "workflow-1",
      isProjectDefault: true,
      name: "Workflow",
      validForTaskCreation: true,
      validationErrors: [],
      version: 1,
    },
    workflows: [],
  };
}

function column() {
  return {
    assigneeRole: "",
    id: "node-1",
    isBacklog: false,
    isDone: false,
    key: "node",
    kind: "agent",
    name: "Node",
    outputFields: [],
    sortOrder: 1,
    transitionOutputFields: [],
    taskCount: 1,
    groupID: "",
  };
}

function cardsPage(): BoardNodeCardsPage {
  return {
    cards: [
      {
        actions: {
          canDelete: true,
          canInterrupt: false,
          canResume: false,
          canStart: true,
          manualMoveTargetNodeIDs: [],
        },
        activeNodeIDs: ["node-1"],
        dependencyProgress: null,
        id: "task-1",
        labelIDs: [],
        preview: { markdown: "", truncated: false },
        shortID: "PRO-1",
        sourceWorkspace: {
          availability: "available",
          id: "workspace-1",
          isPrimary: true,
          name: "Workspace",
          rootPath: "/workspace",
          updatedAt: 1,
        },
        status: {
          attentionTypes: [],
          kind: "active",
          nativeState: "active",
          nodeIDs: ["node-1"],
        },
        title: "Task",
        updatedAt: 1,
        workflowID: "workflow-1",
      },
    ],
    generatedAt: 1,
    nextPageToken: "next",
    nodeID: "node-1",
    previousPageToken: "previous",
    projectID: "project-1",
    workflowID: "workflow-1",
  };
}
