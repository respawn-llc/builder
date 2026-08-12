import { act, fireEvent, render, screen } from "@testing-library/react";

import { VirtualizedGroupedGrid, type VirtualizedGroupedGridEntry } from "./VirtualizedGroupedGrid";

const resizeObservers = vi.hoisted<{
  callbacks: ResizeObserverCallback[];
  current: ResizeObserver | undefined;
}>(() => ({
  callbacks: [],
  current: undefined,
}));
const virtualizer = vi.hoisted(() => ({
  getOffsetForIndex: vi.fn(),
  getTotalSize: vi.fn(() => 240),
  getVirtualItems: vi.fn<
    () => readonly Readonly<{
      end: number;
      index: number;
      key: string;
      lane: number;
      size: number;
      start: number;
    }>[]
  >(() => []),
  measureElement: vi.fn(),
  scrollToIndex: vi.fn(),
  scrollToOffset: vi.fn(),
  shouldAdjustScrollPositionOnItemSizeChange: undefined,
}));
const virtualizerOptions = vi.hoisted<{
  current: Readonly<{ count: number; getItemKey: (index: number) => string }> | undefined;
  useCount: number;
}>(() => ({ current: undefined, useCount: 0 }));

vi.mock("@tanstack/react-virtual", () => ({
  defaultRangeExtractor: ({ startIndex, endIndex }: Readonly<{ startIndex: number; endIndex: number }>) =>
    Array.from({ length: endIndex - startIndex + 1 }, (_value, index) => startIndex + index),
  useVirtualizer: (options: Readonly<{ count: number; getItemKey: (index: number) => string }>) => {
    virtualizerOptions.current = options;
    virtualizerOptions.useCount += 1;
    return virtualizer;
  },
}));

const entries: readonly VirtualizedGroupedGridEntry[] = [
  {
    kind: "column-header",
    key: "columns",
    cells: [
      { key: "status", content: "Status" },
      { key: "title", content: "Title" },
    ],
  },
  {
    kind: "group-header",
    key: "group-active",
    groupKey: "active",
    label: "Active",
    count: 1,
    ariaLabel: "Active, 1 task",
    expanded: true,
    onToggle: () => undefined,
  },
  {
    kind: "task",
    key: "task-1",
    groupKey: "active",
    ariaLabel: "Task KENT-1",
    cells: [
      { key: "status", content: "Running", ariaLabel: "Status: Running" },
      { key: "title", content: "Build grouped grid" },
    ],
  },
];

const makeScrollable = (element: HTMLDivElement | null) => {
  if (element === null) return;
  Object.defineProperties(element, {
    clientHeight: { configurable: true, value: 100 },
    scrollHeight: { configurable: true, value: 300 },
    scrollTop: { configurable: true, value: 40, writable: true },
  });
};

describe("VirtualizedGroupedGrid", () => {
  const originalResizeObserver = globalThis.ResizeObserver;

  beforeAll(() => {
    globalThis.ResizeObserver = class implements ResizeObserver {
      constructor(callback: ResizeObserverCallback) {
        resizeObservers.callbacks.push(callback);
        resizeObservers.current = this;
      }

      disconnect() {
        return undefined;
      }
      observe() {
        return undefined;
      }
      unobserve() {
        return undefined;
      }
    };
  });

  afterAll(() => {
    globalThis.ResizeObserver = originalResizeObserver;
  });

  beforeEach(() => {
    resizeObservers.callbacks = [];
    resizeObservers.current = undefined;
    virtualizer.getVirtualItems.mockReturnValue([]);
    virtualizer.scrollToIndex.mockClear();
    virtualizer.scrollToOffset.mockClear();
    virtualizerOptions.current = undefined;
    virtualizerOptions.useCount = 0;
  });

  it("renders a typed grouped sequence with accessible grid semantics and one virtualizer", () => {
    render(
      <VirtualizedGroupedGrid
        ariaLabel="Project tasks"
        columnCount={2}
        entries={entries}
        estimateSize={() => 40}
      />,
    );

    expect(screen.getByRole("grid", { name: "Project tasks" })).toBeInTheDocument();
    expect(screen.getAllByRole("row")).toHaveLength(3);
    expect(screen.getAllByRole("columnheader")).toHaveLength(2);
    expect(screen.getAllByRole("gridcell")).toHaveLength(3);
    expect(screen.getByRole("row", { name: "Task KENT-1" })).toHaveAccessibleName("Task KENT-1");
    expect(screen.getByRole("gridcell", { name: "Status: Running" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Active.*1/ })).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(screen.getByRole("button", { name: /Active.*1/ }));
    expect(screen.getAllByRole("grid")).toHaveLength(1);
    expect(
      Array.from({ length: virtualizerOptions.current?.count ?? 0 }, (_value, index) =>
        virtualizerOptions.current?.getItemKey(index),
      ),
    ).toEqual(["columns", "group-active", "task-1"]);
  });

  it("routes pointer activation through a Task row while allowing cell actions to stop it", () => {
    const onActivate = vi.fn();
    const onCellActivate = vi.fn();
    const taskEntry: VirtualizedGroupedGridEntry = {
      kind: "task",
      key: "task-1",
      groupKey: "active",
      ariaLabel: "Task KENT-1",
      onActivate,
      onKeyDown(event) {
        if (event.key === "Enter" || event.key === " ") {
          onActivate();
        }
      },
      cells: [
        { key: "status", content: "Running" },
        {
          key: "title",
          content: (
            <button
              onClick={(event) => {
                event.stopPropagation();
                onCellActivate();
              }}
              type="button"
            >
              Edit labels
            </button>
          ),
        },
      ],
    };
    render(
      <VirtualizedGroupedGrid
        ariaLabel="Project tasks"
        columnCount={2}
        entries={[...entries.slice(0, 2), taskEntry]}
        estimateSize={() => 40}
      />,
    );

    fireEvent.click(screen.getByRole("row", { name: "Task KENT-1" }));
    expect(onActivate).toHaveBeenCalledOnce();

    fireEvent.keyDown(screen.getByRole("row", { name: "Task KENT-1" }), { key: "Enter" });
    fireEvent.keyDown(screen.getByRole("row", { name: "Task KENT-1" }), { key: " " });
    expect(onActivate).toHaveBeenCalledTimes(3);

    fireEvent.click(screen.getByRole("button", { name: "Edit labels" }));
    expect(onCellActivate).toHaveBeenCalledOnce();
    expect(onActivate).toHaveBeenCalledTimes(3);
  });

  it("routes visible paging boundaries to their owning group only", () => {
    const loadActive = vi.fn();
    const loadBacklog = vi.fn();
    virtualizer.getVirtualItems.mockReturnValue([
      { end: 160, index: 3, key: "active-next", lane: 0, size: 40, start: 120 },
    ]);

    render(
      <VirtualizedGroupedGrid
        ariaLabel="Project tasks"
        columnCount={2}
        entries={[
          ...entries,
          {
            kind: "boundary",
            key: "active-next",
            groupKey: "active",
            direction: "next",
            hasMore: true,
            isFetching: false,
            loadingLabel: "Loading Active",
            onLoadMore: loadActive,
          },
          {
            kind: "boundary",
            key: "backlog-next",
            groupKey: "backlog",
            direction: "next",
            hasMore: true,
            isFetching: false,
            loadingLabel: "Loading Backlog",
            onLoadMore: loadBacklog,
          },
        ]}
        estimateSize={() => 40}
      />,
    );

    expect(loadActive).toHaveBeenCalledOnce();
    expect(loadBacklog).not.toHaveBeenCalled();
  });

  it("keeps the column header sticky-compatible and restores a pixel offset", () => {
    render(
      <VirtualizedGroupedGrid
        ariaLabel="Project tasks"
        columnCount={2}
        entries={entries}
        estimateSize={() => 40}
        pixelOffsetRequest={{ key: "restore-grid", offsetPx: 160 }}
      />,
    );

    expect(screen.getAllByRole("row")[0]).toHaveClass("sticky");
    expect(virtualizer.scrollToOffset).toHaveBeenCalledWith(160, { behavior: "auto" });
  });

  it("uses absolute top and the keyed disclosure-aware final entry for visual navigation", () => {
    render(
      <VirtualizedGroupedGrid
        ariaLabel="Project tasks"
        columnCount={2}
        entries={entries}
        estimateSize={() => 40}
        onScrollElementChange={makeScrollable}
        navigation={{
          downLabel: "Jump to bottom",
          finalEntryKey: "task-1",
          upLabel: "Jump to top",
        }}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Jump to top" }));
    expect(virtualizer.scrollToOffset).toHaveBeenCalledWith(0, { behavior: "auto" });

    fireEvent.click(screen.getByRole("button", { name: "Jump to bottom" }));
    expect(virtualizer.scrollToIndex).toHaveBeenCalledWith(2, {
      align: "end",
      behavior: "auto",
    });

    fireEvent.click(screen.getByRole("button", { name: "Jump to top" }));
    expect(virtualizer.scrollToOffset).toHaveBeenCalledTimes(2);
  });

  it("targets the lowest visible group header when that disclosure is collapsed", () => {
    render(
      <VirtualizedGroupedGrid
        ariaLabel="Project tasks"
        columnCount={2}
        entries={[
          ...entries,
          {
            kind: "group-header",
            key: "group-done",
            groupKey: "done",
            label: "Done",
            count: 4,
            ariaLabel: "Done, 4 tasks",
            expanded: false,
            onToggle: () => undefined,
          },
        ]}
        estimateSize={() => 40}
        onScrollElementChange={makeScrollable}
        navigation={{
          downLabel: "Jump to bottom",
          finalEntryKey: "group-done",
          upLabel: "Jump to top",
        }}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Jump to bottom" }));
    expect(virtualizer.scrollToIndex).toHaveBeenCalledWith(3, {
      align: "end",
      behavior: "auto",
    });
    expect(screen.getByRole("button", { name: "Done, 4 tasks" })).toHaveAttribute("aria-expanded", "false");
  });

  it("disables keyed navigation until its final entry is present", () => {
    const view = render(
      <VirtualizedGroupedGrid
        ariaLabel="Project tasks"
        columnCount={2}
        entries={entries.slice(0, 2)}
        estimateSize={() => 40}
        onScrollElementChange={makeScrollable}
        navigation={{
          downLabel: "Jump to bottom",
          finalEntryKey: "task-1",
          upLabel: "Jump to top",
        }}
      />,
    );

    expect(screen.getByRole("button", { name: "Jump to bottom" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Jump to bottom" }));
    expect(virtualizer.scrollToIndex).not.toHaveBeenCalled();

    view.rerender(
      <VirtualizedGroupedGrid
        ariaLabel="Project tasks"
        columnCount={2}
        entries={entries}
        estimateSize={() => 40}
        onScrollElementChange={makeScrollable}
        navigation={{
          downLabel: "Jump to bottom",
          finalEntryKey: "task-1",
          upLabel: "Jump to top",
        }}
      />,
    );

    expect(screen.getByRole("button", { name: "Jump to bottom" })).toBeEnabled();
  });

  it("updates navigation visibility when the scrollport viewport resizes", () => {
    let clientHeight = 100;
    const setGeometry = (element: HTMLDivElement | null) => {
      if (element === null) return;
      Object.defineProperties(element, {
        clientHeight: { configurable: true, get: () => clientHeight },
        scrollHeight: { configurable: true, value: 300 },
        scrollTop: { configurable: true, value: 40, writable: true },
      });
    };
    render(
      <VirtualizedGroupedGrid
        ariaLabel="Project tasks"
        columnCount={2}
        entries={entries}
        estimateSize={() => 40}
        onScrollElementChange={setGeometry}
        navigation={{
          downLabel: "Jump to bottom",
          finalEntryKey: "task-1",
          upLabel: "Jump to top",
        }}
      />,
    );

    expect(screen.getByRole("button", { name: "Jump to bottom" })).toBeInTheDocument();
    clientHeight = 400;
    act(() => {
      const observer = resizeObservers.current;
      if (observer !== undefined) resizeObservers.callbacks[0]?.([], observer);
    });

    expect(screen.queryByRole("button", { name: "Jump to bottom" })).not.toBeInTheDocument();
  });
});
