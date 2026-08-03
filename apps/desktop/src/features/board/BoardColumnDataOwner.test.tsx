import { StrictMode } from "react";
import { render, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { canonicalBoardFilter, type BoardColumn, type BoardFilter, type SelectedWorkflowBoard } from "@/api";

const { loggerAppend } = vi.hoisted(() => ({
  loggerAppend: vi.fn(async () => undefined),
}));

const queryByColumn = new Map<string, Record<string, unknown>>();
const runtime = {
  snapshot: {
    active: {
      generation: 2,
      filter: canonicalBoardFilter({ labelFilter: { kind: "none" }, dependencyFilter: true }),
      retiring: false,
    },
    desiredFilter: null as BoardFilter | null,
  },
  controller: {
    getSnapshot: () => runtime.snapshot,
  },
};

vi.mock("@/app-facade", () => ({
  useAppServices: () => ({ logger: { append: loggerAppend } }),
}));

vi.mock("@/shared/labels", () => ({
  useProjectLabelCatalog: () => ({ data: { labels: [] } }),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("./BoardFilterGenerationRuntime", () => ({
  useBoardFilterGeneration: () => runtime,
}));

vi.mock("./useBoardData", () => ({
  useBoardNodeCards: (_projectID: string, _workflowID: string, columnID: string) =>
    queryByColumn.get(columnID),
}));

import { BoardColumnDataOwner, type BoardColumnDataView } from "./BoardColumnDataOwner";

const board = {
  attachedWorkspaceCount: 0,
  defaultWorkspaceID: null,
  projectID: "project-1",
  selectedWorkflow: { id: "workflow-1" },
} as unknown as SelectedWorkflowBoard;
const column = { id: "column-1", taskCount: 1 } as unknown as BoardColumn;

afterEach(() => {
  queryByColumn.clear();
  loggerAppend.mockClear();
  runtime.snapshot = {
    active: {
      generation: 2,
      filter: canonicalBoardFilter({ labelFilter: { kind: "none" }, dependencyFilter: true }),
      retiring: false,
    },
    desiredFilter: null,
  };
});

describe("BoardColumnDataOwner retained replacement boundary", () => {
  it("publishes a generation-scoped Retry boundary, logs the failure, and removes it after recovery", async () => {
    const refetch = vi.fn(async () => undefined);
    queryByColumn.set("column-1", queryState({ refetch }));
    let latestView: BoardColumnDataView | undefined;
    const view = render(
      <StrictMode>
        <Owner onView={(next) => (latestView = next)} />
      </StrictMode>,
    );
    await waitFor(() => expect(latestView?.replacementBoundary).toBeUndefined());

    runtime.snapshot = {
      ...runtime.snapshot,
      active: { ...runtime.snapshot.active, generation: 3 },
    };
    const replacementError = new Error("replacement failed");
    queryByColumn.set("column-1", queryState({ error: replacementError, isError: true, refetch }));
    view.rerender(
      <StrictMode>
        <Owner onView={(next) => (latestView = next)} />
      </StrictMode>,
    );

    await waitFor(() => expect(latestView?.replacementBoundary?.state).toBe("error"));
    expect(loggerAppend).toHaveBeenCalledWith(
      "warn",
      "Board task-card replacement failed.",
      expect.objectContaining({
        columnID: "column-1",
        error: "replacement failed",
        filterGeneration: "3",
        projectID: "project-1",
        workflowID: "workflow-1",
      }),
    );
    expect(loggerAppend).toHaveBeenCalledOnce();
    latestView?.replacementBoundary?.state === "error" && latestView.replacementBoundary.onRetry();
    expect(refetch).toHaveBeenCalledOnce();

    const staleRetry = latestView?.replacementBoundary;
    const replacementFilter = canonicalBoardFilter({
      labelFilter: { kind: "none" },
      dependencyFilter: false,
    });
    runtime.snapshot = {
      active: { generation: 4, filter: replacementFilter, retiring: false },
      desiredFilter: null,
    };
    if (staleRetry?.state === "error") {
      staleRetry.onRetry();
    }
    expect(refetch).toHaveBeenCalledOnce();
    const recoveredQuery = queryState({ refetch });
    queryByColumn.set("column-1", recoveredQuery);
    view.rerender(
      <StrictMode>
        <Owner onView={(next) => (latestView = next)} />
      </StrictMode>,
    );
    await waitFor(() => expect(latestView?.replacementBoundary).toBeUndefined());
    view.unmount();
  });

  it("keeps retained replacement failures independent across visible columns", async () => {
    const firstRefetch = vi.fn(async () => undefined);
    const secondRefetch = vi.fn(async () => undefined);
    const secondColumn = { id: "column-2", taskCount: 1 } as unknown as BoardColumn;
    queryByColumn.set("column-1", queryState({ refetch: firstRefetch }));
    queryByColumn.set("column-2", queryState({ refetch: secondRefetch }));
    const views = new Map<string, BoardColumnDataView>();
    const view = render(
      <>
        <Owner onView={(next) => views.set("column-1", next)} />
        <Owner column={secondColumn} onView={(next) => views.set("column-2", next)} />
      </>,
    );
    await waitFor(() => expect(views.get("column-2")?.replacementBoundary).toBeUndefined());

    runtime.snapshot = {
      ...runtime.snapshot,
      active: { ...runtime.snapshot.active, generation: 3 },
    };
    queryByColumn.set(
      "column-1",
      queryState({ error: new Error("first"), isError: true, refetch: firstRefetch }),
    );
    queryByColumn.set(
      "column-2",
      queryState({ error: new Error("second"), isError: true, refetch: secondRefetch }),
    );
    view.rerender(
      <>
        <Owner onView={(next) => views.set("column-1", next)} />
        <Owner column={secondColumn} onView={(next) => views.set("column-2", next)} />
      </>,
    );

    await waitFor(() => {
      expect(views.get("column-1")?.replacementBoundary?.state).toBe("error");
      expect(views.get("column-2")?.replacementBoundary?.state).toBe("error");
    });
    const firstBoundary = views.get("column-1")?.replacementBoundary;
    if (firstBoundary?.state === "error") {
      firstBoundary.onRetry();
    }
    expect(firstRefetch).toHaveBeenCalledOnce();
    expect(secondRefetch).not.toHaveBeenCalled();
    expect(views.get("column-2")?.replacementBoundary?.state).toBe("error");
    view.unmount();
  });

  it("removes the boundary when the generation retires and releases the view on deactivation", async () => {
    queryByColumn.set("column-1", queryState());
    const onViewRelease = vi.fn();
    let latestView: BoardColumnDataView | undefined;
    const view = render(<Owner onDataViewRelease={onViewRelease} onView={(next) => (latestView = next)} />);
    await waitFor(() => expect(latestView?.replacementBoundary).toBeUndefined());

    runtime.snapshot = {
      ...runtime.snapshot,
      active: { ...runtime.snapshot.active, generation: 3, retiring: true },
      desiredFilter: canonicalBoardFilter({
        labelFilter: { kind: "none" },
        dependencyFilter: false,
      }),
    };
    queryByColumn.set("column-1", queryState({ error: new Error("retiring"), isError: true }));
    view.rerender(<Owner onDataViewRelease={onViewRelease} onView={(next) => (latestView = next)} />);
    await waitFor(() => expect(latestView?.replacementBoundary).toBeUndefined());
    view.unmount();
    expect(onViewRelease).toHaveBeenCalledOnce();
  });
});

function Owner({
  column: ownerColumn = column,
  onDataViewRelease = vi.fn(),
  onView,
}: Readonly<{
  column?: BoardColumn;
  onDataViewRelease?: () => void;
  onView(view: BoardColumnDataView): void;
}>) {
  return (
    <BoardColumnDataOwner
      board={board}
      column={ownerColumn}
      onDataViewChange={onView}
      onDataViewRelease={onDataViewRelease}
      onReportColumnSnapshot={vi.fn()}
    />
  );
}

function queryState(overrides: Readonly<Record<string, unknown>> = {}): Record<string, unknown> {
  return {
    data: { pages: [{ cards: [] }] },
    error: null,
    fetchNextPage: vi.fn(),
    fetchPreviousPage: vi.fn(),
    hasNextPage: false,
    hasPreviousPage: false,
    isError: false,
    isFetchNextPageError: false,
    isFetchPreviousPageError: false,
    isFetching: false,
    isFetchingNextPage: false,
    isFetchingPreviousPage: false,
    isPending: false,
    isPlaceholderData: false,
    refetch: vi.fn(async () => undefined),
    ...overrides,
  };
}
