import { render, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { BoardColumn } from "@/api";

type LogContext = Readonly<Record<string, string>>;

const { loggerAppend } = vi.hoisted(() => ({
  loggerAppend: vi.fn(async (...args: ["warn", string, LogContext]): Promise<void> => {
    await Promise.resolve(args);
  }),
}));

const queryByColumn = new Map<string, Record<string, unknown>>();

vi.mock("@/app-facade", () => ({
  useAppServices: () => ({ logger: { append: loggerAppend } }),
}));

vi.mock("@/shared/labels", () => ({
  useProjectLabelCatalog: () => ({ data: { labels: [] } }),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("./useBoardData", () => ({
  useBoardNodeCards: (_projectID: string, _workflowID: string, columnID: string) =>
    queryByColumn.get(columnID),
}));

import { BoardColumnDataOwner, type BoardColumnDataView } from "./BoardColumnDataOwner";

const board = {
  attachedWorkspaceCount: 0,
  defaultWorkspaceID: "workspace-1",
  projectID: "project-1",
  selectedWorkflow: { id: "workflow-1" },
};
const column: BoardColumn = {
  id: "column-1",
  key: "column-1",
  kind: "agent",
  name: "Column",
  assigneeRole: "",
  outputFields: [],
  groupID: "group-1",
  sortOrder: 0,
  isBacklog: false,
  isDone: false,
  taskCount: 1,
};

afterEach(() => {
  queryByColumn.clear();
  loggerAppend.mockClear();
});

describe("BoardColumnDataOwner retained replacement boundary", () => {
  it("publishes a Retry boundary, logs the failure, and removes it after recovery", async () => {
    const refetch = vi.fn(async () => undefined);
    const onViewRelease = vi.fn();
    queryByColumn.set("column-1", queryState({ refetch }));
    let latestView: BoardColumnDataView | undefined;
    const view = render(
      <Owner
        onDataViewRelease={onViewRelease}
        onView={(next) => {
          latestView = next;
        }}
      />,
    );
    await waitFor(() => {
      expect(latestView?.replacementBoundary).toBeUndefined();
    });
    queryByColumn.set(
      "column-1",
      queryState({ error: new Error(), isError: true, isPlaceholderData: true, refetch }),
    );
    view.rerender(
      <Owner
        onDataViewRelease={onViewRelease}
        onView={(next) => {
          latestView = next;
        }}
      />,
    );
    await waitFor(() => {
      expect(latestView?.replacementBoundary?.state).toBe("error");
    });
    const logCall = loggerAppend.mock.calls[0];
    expect(typeof logCall?.[1]).toBe("string");
    const diagnostic = logCall?.[2];
    expect(diagnostic).toEqual(
      expect.objectContaining({
        columnID: "column-1",
        projectID: "project-1",
        workflowID: "workflow-1",
      }),
    );
    expect(typeof diagnostic?.error).toBe("string");
    if (latestView?.replacementBoundary?.state === "error") {
      latestView.replacementBoundary.onRetry();
    }
    expect(refetch).toHaveBeenCalledOnce();

    queryByColumn.set("column-1", queryState({ refetch }));
    view.rerender(
      <Owner
        onDataViewRelease={onViewRelease}
        onView={(next) => {
          latestView = next;
        }}
      />,
    );
    await waitFor(() => {
      expect(latestView?.replacementBoundary).toBeUndefined();
    });
    view.unmount();
    expect(onViewRelease).toHaveBeenCalledOnce();
  });

  it("keeps retained replacement failures independent across visible columns", async () => {
    const firstRefetch = vi.fn(async () => undefined);
    const secondRefetch = vi.fn(async () => undefined);
    const secondColumn = { ...column, id: "column-2" };
    queryByColumn.set("column-1", queryState({ refetch: firstRefetch }));
    queryByColumn.set("column-2", queryState({ refetch: secondRefetch }));
    const views = new Map<string, BoardColumnDataView>();
    const view = render(
      <>
        <Owner
          onView={(next) => {
            views.set("column-1", next);
          }}
        />
        <Owner
          column={secondColumn}
          onView={(next) => {
            views.set("column-2", next);
          }}
        />
      </>,
    );
    await waitFor(() => {
      expect(views.get("column-2")?.replacementBoundary).toBeUndefined();
    });

    queryByColumn.set(
      "column-1",
      queryState({ error: new Error(), isError: true, isPlaceholderData: true, refetch: firstRefetch }),
    );
    queryByColumn.set(
      "column-2",
      queryState({ error: new Error(), isError: true, isPlaceholderData: true, refetch: secondRefetch }),
    );
    view.rerender(
      <>
        <Owner
          onView={(next) => {
            views.set("column-1", next);
          }}
        />
        <Owner
          column={secondColumn}
          onView={(next) => {
            views.set("column-2", next);
          }}
        />
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
    isError: false,
    isFetching: false,
    isPending: false,
    isPlaceholderData: false,
    refetch: vi.fn(async () => undefined),
    ...overrides,
  };
}
