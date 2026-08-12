import { fireEvent, render, screen } from "@testing-library/react";
import { useLayoutEffect } from "react";

import { VirtualizedInfiniteList } from "./VirtualizedInfiniteList";
import { VirtualizedFrame } from "./VirtualizedFrame";
import {
  createVirtualizedPixelOffsetRequest,
  type VirtualizedPixelOffsetRequest,
} from "./virtualizedPixelOffsetRequest";

const virtualizer = vi.hoisted(() => ({
  getOffsetForIndex: vi.fn(),
  getTotalSize: vi.fn(() => 120),
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
  current:
    | Readonly<{
        count: number;
        getItemKey: (index: number) => string;
      }>
    | undefined;
  useCount: number;
}>(() => ({
  current: undefined,
  useCount: 0,
}));
const testEstimateSize = () => 40;
const testGetItemKey = (item: string) => item;
const testRenderItem = (item: string) => item;

vi.mock("@tanstack/react-virtual", () => ({
  defaultRangeExtractor: ({ startIndex, endIndex }: Readonly<{ startIndex: number; endIndex: number }>) =>
    Array.from({ length: endIndex - startIndex + 1 }, (_value, index) => startIndex + index),
  useVirtualizer: (options: Readonly<{ count: number; getItemKey: (index: number) => string }>) => {
    virtualizerOptions.current = options;
    virtualizerOptions.useCount += 1;
    return virtualizer;
  },
}));

function List({
  hasNextPage = false,
  onLoadMore = () => undefined,
  hasPreviousPage = false,
  onLoadPrevious = () => undefined,
  previousLoadItemKey,
  previousBoundary,
  nextBoundary,
  items = ["a", "b", "c"],
  ready = true,
  request,
  onScrollElementChange,
}: Readonly<{
  hasNextPage?: boolean;
  onLoadMore?: () => void;
  hasPreviousPage?: boolean;
  onLoadPrevious?: () => void;
  previousLoadItemKey?: string;
  previousBoundary?:
    | { state: "loading"; label: string }
    | { state: "error"; message: string; retryLabel: string; onRetry: () => void };
  nextBoundary?:
    | { state: "loading"; label: string }
    | { state: "error"; message: string; retryLabel: string; onRetry: () => void };
  items?: readonly string[];
  ready?: boolean;
  request?: VirtualizedPixelOffsetRequest | undefined;
  onScrollElementChange?: (element: HTMLDivElement | null) => void;
}>) {
  return (
    <VirtualizedInfiniteList
      estimateSize={testEstimateSize}
      getItemKey={testGetItemKey}
      hasNextPage={hasNextPage}
      hasPreviousPage={hasPreviousPage}
      isFetchingNextPage={false}
      isFetchingPreviousPage={false}
      items={items}
      loadingLabel="Loading"
      nextBoundary={nextBoundary}
      onLoadMore={onLoadMore}
      onLoadPrevious={onLoadPrevious}
      onScrollElementChange={onScrollElementChange}
      previousBoundary={previousBoundary}
      previousLoadItemKey={previousLoadItemKey}
      pixelOffsetRequest={ready ? request : undefined}
      renderItem={testRenderItem}
    />
  );
}

describe("VirtualizedInfiniteList pixel restoration", () => {
  beforeEach(() => {
    virtualizer.getVirtualItems.mockReturnValue([]);
    virtualizer.getOffsetForIndex.mockClear();
    virtualizer.scrollToOffset.mockClear();
    virtualizerOptions.current = undefined;
    virtualizerOptions.useCount = 0;
  });

  it("preserves list semantics and stable keys for every single-stream row kind", () => {
    render(
      <VirtualizedInfiniteList
        empty="No items"
        estimateSize={testEstimateSize}
        getItemKey={testGetItemKey}
        hasNextPage={false}
        header="Header"
        isFetchingNextPage={false}
        items={[]}
        loadingLabel="Loading"
        nextBoundary={{ state: "loading", label: "Loading next" }}
        onLoadMore={() => undefined}
        previousBoundary={{ state: "loading", label: "Loading previous" }}
        renderItem={testRenderItem}
      />,
    );

    expect(screen.getByRole("list")).toBeInTheDocument();
    expect(screen.getAllByRole("listitem")).toHaveLength(4);
    expect(virtualizerOptions.current?.count).toBe(4);
    expect(
      Array.from({ length: virtualizerOptions.current?.count ?? 0 }, (_value, index) =>
        virtualizerOptions.current?.getItemKey(index),
      ),
    ).toEqual(["boundary-previous", "header", "empty", "boundary-next"]);
  });

  it("preserves listbox presentation rows and creates only one virtualizer", () => {
    render(
      <VirtualizedInfiniteList
        estimateSize={testEstimateSize}
        getItemKey={testGetItemKey}
        hasNextPage={false}
        isFetchingNextPage={false}
        items={["a", "b"]}
        loadingLabel="Loading"
        onLoadMore={() => undefined}
        renderItem={(item) => <div role="option">{item}</div>}
        role="listbox"
      />,
    );

    expect(screen.getByRole("listbox")).toBeInTheDocument();
    expect(screen.getAllByRole("presentation")).toHaveLength(2);
    expect(screen.getAllByRole("option")).toHaveLength(2);
    expect(virtualizerOptions.useCount).toBe(1);
    expect(virtualizerOptions.current?.getItemKey(0)).toBe("a");
    expect(virtualizerOptions.current?.getItemKey(1)).toBe("b");
  });

  it("lets a shared frame adapter supply grid and row semantics without another virtualizer", () => {
    render(
      <VirtualizedFrame
        canApplyPixelOffset={false}
        entries={[
          {
            key: "row-a",
            render: () => <div role="gridcell">A</div>,
          },
        ]}
        estimateSize={testEstimateSize}
        initialScrollAlign="start"
        paddingEnd={0}
        paddingStart={0}
        role="grid"
        rowRole="row"
        rowSpacing="tight"
      />,
    );

    expect(screen.getByRole("grid")).toBeInTheDocument();
    expect(screen.getByRole("row")).toContainElement(screen.getByRole("gridcell"));
    expect(virtualizerOptions.useCount).toBe(1);
  });

  it("applies each valid request key once through the virtualizer after rows mount", () => {
    const request = createVirtualizedPixelOffsetRequest("restore-1", 240);
    const view = render(<List ready={false} request={request} />);
    expect(virtualizer.scrollToOffset).not.toHaveBeenCalled();

    view.rerender(<List request={request} />);
    expect(virtualizer.scrollToOffset).toHaveBeenCalledWith(240, { behavior: "auto" });

    view.rerender(<List request={createVirtualizedPixelOffsetRequest("restore-1", 480)} />);
    expect(virtualizer.scrollToOffset).toHaveBeenCalledTimes(1);

    view.rerender(<List request={createVirtualizedPixelOffsetRequest("restore-2", 480)} />);
    expect(virtualizer.scrollToOffset).toHaveBeenLastCalledWith(480, { behavior: "auto" });
  });

  it("restores pixels before an owning layout effect can capture the initial anchor", () => {
    let capturedScrollTop: number | undefined;
    function LayoutOwner() {
      useLayoutEffect(() => {
        capturedScrollTop = screen.getByRole("list").scrollTop;
      }, []);
      return <List request={createVirtualizedPixelOffsetRequest("layout-order", 240)} />;
    }

    render(<LayoutOwner />);

    expect(capturedScrollTop).toBe(240);
  });

  it("preserves the restored anchor when a later layout resets the scroll element", () => {
    const items = Array.from({ length: 20 }, (_value, index) => `item-${index.toString()}`);
    const request = createVirtualizedPixelOffsetRequest("layout-reset", 240);
    const view = render(<List items={items} request={request} />);
    const list = screen.getByRole("list");
    list.scrollTop = 0;
    virtualizer.getVirtualItems.mockReturnValue([
      { end: 280, index: 6, key: "item-6", lane: 0, size: 40, start: 240 },
    ]);

    view.rerender(<List items={items} request={request} />);

    expect(list.scrollTop).toBe(240);
  });

  it("restores a moved row through its separate anchor identity", () => {
    const initialItems = [
      { anchor: "row-a", slot: "slot-a" },
      { anchor: "row-b", slot: "slot-b" },
      { anchor: "row-c", slot: "slot-c" },
    ];
    const refreshedItems = [{ anchor: "row-new", slot: "slot-new" }, ...initialItems];
    virtualizer.getOffsetForIndex.mockImplementation((index: number) => [index * 40]);
    virtualizer.getVirtualItems.mockReturnValue([
      { end: 80, index: 1, key: "slot-b", lane: 0, size: 40, start: 40 },
    ]);
    const view = render(
      <VirtualizedInfiniteList
        estimateSize={testEstimateSize}
        getItemAnchorKey={(item) => item.anchor}
        getItemKey={(item) => item.slot}
        hasNextPage={false}
        isFetchingNextPage={false}
        items={initialItems}
        loadingLabel="Loading"
        onLoadMore={() => undefined}
        renderItem={(item) => item.slot}
      />,
    );
    const list = screen.getByRole("list");
    list.scrollTop = 40;
    fireEvent.scroll(list);

    virtualizer.getVirtualItems.mockReturnValue([
      { end: 120, index: 2, key: "slot-b", lane: 0, size: 40, start: 80 },
    ]);
    view.rerender(
      <VirtualizedInfiniteList
        estimateSize={testEstimateSize}
        getItemAnchorKey={(item) => item.anchor}
        getItemKey={(item) => item.slot}
        hasNextPage={false}
        isFetchingNextPage={false}
        items={refreshedItems}
        loadingLabel="Loading"
        onLoadMore={() => undefined}
        renderItem={(item) => item.slot}
      />,
    );

    expect(virtualizer.getOffsetForIndex).toHaveBeenCalledWith(2, "start");
    expect(list.scrollTop).toBe(80);
  });

  it("uses occurrence identity before falling back to a duplicate row anchor", () => {
    const initialItems = [
      { anchor: "row", slot: "slot-a" },
      { anchor: "row", slot: "slot-b" },
    ];
    const refreshedItems = [{ anchor: "row-new", slot: "slot-new" }, ...initialItems];
    virtualizer.getOffsetForIndex.mockImplementation((index: number) => [index * 40]);
    virtualizer.getVirtualItems.mockReturnValue([
      { end: 80, index: 1, key: "slot-b", lane: 0, size: 40, start: 40 },
    ]);
    const view = render(
      <VirtualizedInfiniteList
        estimateSize={testEstimateSize}
        getItemAnchorKey={(item) => item.anchor}
        getItemKey={(item) => item.slot}
        getItemOccurrenceKey={(item) => item.slot}
        hasNextPage={false}
        isFetchingNextPage={false}
        items={initialItems}
        loadingLabel="Loading"
        onLoadMore={() => undefined}
        renderItem={(item) => item.slot}
      />,
    );
    const list = screen.getByRole("list");
    list.scrollTop = 40;
    fireEvent.scroll(list);

    virtualizer.getVirtualItems.mockReturnValue([
      { end: 120, index: 2, key: "slot-b", lane: 0, size: 40, start: 80 },
    ]);
    view.rerender(
      <VirtualizedInfiniteList
        estimateSize={testEstimateSize}
        getItemAnchorKey={(item) => item.anchor}
        getItemKey={(item) => item.slot}
        getItemOccurrenceKey={(item) => item.slot}
        hasNextPage={false}
        isFetchingNextPage={false}
        items={refreshedItems}
        loadingLabel="Loading"
        onLoadMore={() => undefined}
        renderItem={(item) => item.slot}
      />,
    );

    expect(virtualizer.getOffsetForIndex).toHaveBeenCalledWith(2, "start");
    expect(list.scrollTop).toBe(80);
  });

  it("ignores a stale virtual row that starts after the restored offset", () => {
    const items = Array.from({ length: 20 }, (_value, index) => `item-${index.toString()}`);
    const request = createVirtualizedPixelOffsetRequest("stale-range", 240);
    virtualizer.getVirtualItems.mockReturnValue([
      { end: 360, index: 0, key: "item-0", lane: 0, size: 40, start: 320 },
    ]);
    const view = render(<List items={items} request={request} />);
    const list = screen.getByRole("list");
    expect(list.scrollTop).toBe(240);

    virtualizer.getVirtualItems.mockReturnValue([
      { end: 280, index: 6, key: "item-6", lane: 0, size: 40, start: 240 },
    ]);
    view.rerender(<List items={items} request={request} />);

    expect(list.scrollTop).toBe(240);
  });

  it("accepts a clamped retained offset without chasing later virtual-item changes", () => {
    const items = Array.from({ length: 20 }, (_value, index) => `item-${index.toString()}`);
    let scrollTop = 0;
    const clampScrollElement = (element: HTMLDivElement | null) => {
      if (element === null) return;
      Object.defineProperty(element, "scrollTop", {
        configurable: true,
        get: () => scrollTop,
        set: (value: number) => {
          scrollTop = Math.min(value, 160);
        },
      });
    };
    const view = render(
      <List
        items={items}
        onScrollElementChange={clampScrollElement}
        request={createVirtualizedPixelOffsetRequest("clamped", 640)}
      />,
    );
    expect(screen.getByRole("list").scrollTop).toBe(160);

    virtualizer.getVirtualItems.mockReturnValue([
      { end: 40, index: 0, key: "item-0", lane: 0, size: 40, start: 0 },
    ]);
    view.rerender(
      <List
        items={items}
        onScrollElementChange={clampScrollElement}
        request={createVirtualizedPixelOffsetRequest("clamped", 640)}
      />,
    );

    expect(screen.getByRole("list").scrollTop).toBe(160);
  });

  it("does not overwrite a user scroll after restoration", () => {
    const items = Array.from({ length: 20 }, (_value, index) => `item-${index.toString()}`);
    const view = render(
      <List items={items} request={createVirtualizedPixelOffsetRequest("user-scroll", 240)} />,
    );
    const list = screen.getByRole("list");
    list.scrollTop = 640;
    fireEvent.scroll(list);

    view.rerender(<List items={items} request={createVirtualizedPixelOffsetRequest("user-scroll", 240)} />);

    expect(list.scrollTop).toBe(640);
  });

  it("accepts absence without issuing a restoration request", () => {
    render(<List />);
    expect(virtualizer.scrollToOffset).not.toHaveBeenCalled();
  });

  it.each([
    { key: "", offsetPx: 10 },
    { key: "negative", offsetPx: -1 },
  ])("fails invalid restoration requests", (request) => {
    expect(() => render(<List request={request} />)).toThrow();
  });

  it("loads only when the restored visible range reaches the loaded edge", () => {
    const onLoadMore = vi.fn();
    virtualizer.getVirtualItems.mockReturnValue([
      { end: 40, index: 0, key: "a", lane: 0, size: 40, start: 0 },
    ]);
    const view = render(
      <List hasNextPage onLoadMore={onLoadMore} request={createVirtualizedPixelOffsetRequest("deep", 999)} />,
    );

    expect(virtualizer.scrollToOffset).toHaveBeenCalledWith(999, { behavior: "auto" });
    expect(onLoadMore).not.toHaveBeenCalled();

    virtualizer.getVirtualItems.mockReturnValue([
      { end: 120, index: 2, key: "c", lane: 0, size: 40, start: 80 },
    ]);
    view.rerender(
      <List hasNextPage onLoadMore={onLoadMore} request={createVirtualizedPixelOffsetRequest("deep", 999)} />,
    );
    expect(screen.getByText("c")).toBeInTheDocument();
    expect(onLoadMore).toHaveBeenCalledOnce();
  });

  it("keeps a supplied newer boundary at list start while loading from the first item key", () => {
    const onLoadPrevious = vi.fn();
    virtualizer.getVirtualItems.mockReturnValue([
      { end: 40, index: 0, key: "a", lane: 0, size: 40, start: 0 },
      { end: 80, index: 1, key: "boundary-previous", lane: 0, size: 40, start: 40 },
      { end: 120, index: 2, key: "b", lane: 0, size: 40, start: 80 },
    ]);
    render(
      <List
        hasPreviousPage
        onLoadPrevious={onLoadPrevious}
        previousBoundary={{ state: "loading", label: "Loading" }}
        previousLoadItemKey="b"
      />,
    );
    expect(onLoadPrevious).toHaveBeenCalledOnce();
    const boundary = screen.getByTestId("virtual-boundary-previous");
    const rows = screen.getAllByRole("listitem");
    expect(rows[0]).toContainElement(boundary);
    expect(rows[1]).toHaveTextContent("a");
  });

  it("triggers newer loading only when the first feed item is visible", () => {
    const onLoadPrevious = vi.fn();
    virtualizer.getVirtualItems.mockReturnValue([
      { end: 40, index: 0, key: "fixed", lane: 0, size: 40, start: 0 },
    ]);
    const view = render(
      <List
        hasPreviousPage
        items={["fixed", "feed"]}
        onLoadPrevious={onLoadPrevious}
        previousLoadItemKey="feed"
      />,
    );
    expect(onLoadPrevious).not.toHaveBeenCalled();

    virtualizer.getVirtualItems.mockReturnValue([
      { end: 40, index: 0, key: "fixed", lane: 0, size: 40, start: 0 },
      { end: 80, index: 1, key: "feed", lane: 0, size: 40, start: 40 },
    ]);
    view.rerender(
      <List
        hasPreviousPage
        items={["fixed", "feed"]}
        onLoadPrevious={onLoadPrevious}
        previousLoadItemKey="feed"
      />,
    );
    expect(onLoadPrevious).toHaveBeenCalledOnce();
    expect(screen.queryByTestId("virtual-boundary-previous")).not.toBeInTheDocument();
  });

  it("keeps rows visible and stops automatic older retries until the boundary is retried", () => {
    const onLoadMore = vi.fn();
    const onRetry = vi.fn();
    virtualizer.getVirtualItems.mockReturnValue([
      { end: 40, index: 0, key: "a", lane: 0, size: 40, start: 0 },
      { end: 80, index: 1, key: "b", lane: 0, size: 40, start: 40 },
      { end: 120, index: 2, key: "c", lane: 0, size: 40, start: 80 },
      { end: 160, index: 3, key: "boundary-next", lane: 0, size: 40, start: 120 },
    ]);
    const view = render(
      <List
        nextBoundary={{
          state: "error",
          message: "page failed",
          onRetry,
          retryLabel: "Retry",
        }}
        onLoadMore={onLoadMore}
      />,
    );

    expect(onLoadMore).not.toHaveBeenCalled();
    expect(screen.getByText("a")).toBeInTheDocument();
    expect(screen.getByText("c")).toBeInTheDocument();
    expect(screen.getByTestId("virtual-boundary-next")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(onRetry).toHaveBeenCalledOnce();

    view.rerender(
      <List
        items={["a", "b", "c", "d"]}
        nextBoundary={{ state: "loading", label: "Loading" }}
        onLoadMore={onLoadMore}
      />,
    );
    expect(screen.getByText("d")).toBeInTheDocument();
    expect(onLoadMore).not.toHaveBeenCalled();
  });
});
