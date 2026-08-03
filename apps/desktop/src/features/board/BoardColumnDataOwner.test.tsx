import { StrictMode } from "react";
import { render, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { canonicalBoardFilter, type BoardColumn, type BoardFilter, type SelectedWorkflowBoard } from "@/api";
import type { BoardColumnNoticeEvent } from "./BoardColumnDataOwner";

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

import { BoardColumnDataOwner } from "./BoardColumnDataOwner";

const board = {
  attachedWorkspaceCount: 0,
  defaultWorkspaceID: null,
  projectID: "project-1",
  selectedWorkflow: { id: "workflow-1" },
} as unknown as SelectedWorkflowBoard;
const column = { id: "column-1", taskCount: 1 } as unknown as BoardColumn;

afterEach(() => {
  queryByColumn.clear();
  runtime.snapshot = {
    active: {
      generation: 2,
      filter: canonicalBoardFilter({ labelFilter: { kind: "none" }, dependencyFilter: true }),
      retiring: false,
    },
    desiredFilter: null,
  };
});

describe("BoardColumnDataOwner retained replacement notices", () => {
  it("keeps Retry bound to the exact active generation and dismisses after recovery", async () => {
    const refetch = vi.fn(async () => undefined);
    queryByColumn.set("column-1", queryState({ refetch }));
    const events: BoardColumnNoticeEvent[] = [];
    const view = render(
      <StrictMode>
        <Owner onNotice={(event) => events.push(event)} />
      </StrictMode>,
    );

    queryByColumn.set(
      "column-1",
      queryState({ error: new Error("replacement failed"), isError: true, isPlaceholderData: true, refetch }),
    );
    view.rerender(
      <StrictMode>
        <Owner onNotice={(event) => events.push(event)} />
      </StrictMode>,
    );
    await waitFor(() => expect(events.at(-1)?.kind).toBe("failure"));

    const failure = events.find(
      (event): event is Extract<BoardColumnNoticeEvent, { kind: "failure" }> => event.kind === "failure",
    );
    expect(typeof failure?.noticeID).toBe("string");
    failure?.retry();
    expect(refetch).toHaveBeenCalledOnce();

    runtime.snapshot = {
      ...runtime.snapshot,
      active: { ...runtime.snapshot.active, generation: 3 },
    };
    failure?.retry();
    expect(refetch).toHaveBeenCalledOnce();

    runtime.snapshot = {
      active: {
        generation: 3,
        filter: canonicalBoardFilter({ labelFilter: { kind: "none" }, dependencyFilter: true }),
        retiring: false,
      },
      desiredFilter: null,
    };
    queryByColumn.set("column-1", queryState({ refetch }));
    view.rerender(
      <StrictMode>
        <Owner onNotice={(event) => events.push(event)} />
      </StrictMode>,
    );
    await waitFor(() => expect(events.at(-1)?.kind).toBe("dismiss"));
  });

  it("keeps visible column notices independent and removes them on owner unmount", async () => {
    const firstRefetch = vi.fn(async () => undefined);
    const secondRefetch = vi.fn(async () => undefined);
    const firstColumn = { id: "column-1", taskCount: 1 } as unknown as BoardColumn;
    const secondColumn = { id: "column-2", taskCount: 1 } as unknown as BoardColumn;
    queryByColumn.set(
      "column-1",
      queryState({
        error: new Error("first"),
        isError: true,
        isPlaceholderData: true,
        refetch: firstRefetch,
      }),
    );
    queryByColumn.set(
      "column-2",
      queryState({
        error: new Error("second"),
        isError: true,
        isPlaceholderData: true,
        refetch: secondRefetch,
      }),
    );
    const events: BoardColumnNoticeEvent[] = [];
    const view = render(
      <>
        <Owner column={firstColumn} onNotice={(event) => events.push(event)} />
        <Owner column={secondColumn} onNotice={(event) => events.push(event)} />
      </>,
    );

    await waitFor(() => expect(events.filter((event) => event.kind === "failure")).toHaveLength(2));
    const failures = events.filter(
      (event): event is Extract<BoardColumnNoticeEvent, { kind: "failure" }> => event.kind === "failure",
    );
    expect(new Set(failures.map((event) => event.noticeID)).size).toBe(2);
    failures[0]?.retry();
    expect(firstRefetch).toHaveBeenCalledOnce();
    expect(secondRefetch).not.toHaveBeenCalled();

    view.unmount();
    expect(events.filter((event) => event.kind === "dismiss")).toHaveLength(2);
  });
});

function Owner({
  column: ownerColumn = column,
  onNotice,
}: Readonly<{
  column?: BoardColumn;
  onNotice(event: BoardColumnNoticeEvent): void;
}>) {
  return (
    <BoardColumnDataOwner
      board={board}
      column={ownerColumn}
      onBoardColumnNotice={onNotice}
      onCardsLoadError={vi.fn()}
      onDataViewChange={vi.fn()}
      onDataViewRelease={vi.fn()}
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
