import { fireEvent, render, screen } from "@testing-library/react";
import { useLayoutEffect } from "react";

import { VirtualizedInfiniteList } from "./VirtualizedInfiniteList";
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
const testEstimateSize = () => 40;
const testGetItemKey = (item: string) => item;
const testRenderItem = (item: string) => item;

vi.mock("@tanstack/react-virtual", () => ({
  defaultRangeExtractor: ({ startIndex, endIndex }: Readonly<{ startIndex: number; endIndex: number }>) =>
    Array.from({ length: endIndex - startIndex + 1 }, (_value, index) => startIndex + index),
  useVirtualizer: () => virtualizer,
}));

function List({
  hasNextPage = false,
  isFetchingNextPage = false,
  isFetchingPreviousPage = false,
  onLoadMore = () => undefined,
  hasPreviousPage = false,
  onLoadPrevious = () => undefined,
  previousLoadItemKey,
  previousBoundary,
  nextBoundary,
  items = ["a", "b", "c"],
  ready = true,
  request,
  initialScrollKey,
  initialScrollRequestKey,
  layoutChangeScrollBehavior,
  onScrollElementChange,
  orientation,
  header,
}: Readonly<{
  hasNextPage?: boolean;
  isFetchingNextPage?: boolean;
  isFetchingPreviousPage?: boolean;
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
  initialScrollKey?: string | undefined;
  initialScrollRequestKey?: string | undefined;
  layoutChangeScrollBehavior?: "preserve-leading-item" | "natural";
  onScrollElementChange?: (element: HTMLDivElement | null) => void;
  orientation?: "vertical" | "horizontal";
  header?: React.ReactNode;
}>) {
  return (
    <VirtualizedInfiniteList
      estimateSize={testEstimateSize}
      getItemKey={testGetItemKey}
      hasNextPage={hasNextPage}
      hasPreviousPage={hasPreviousPage}
      header={header}
      isFetchingNextPage={isFetchingNextPage}
      isFetchingPreviousPage={isFetchingPreviousPage}
      initialScrollKey={initialScrollKey}
      initialScrollRequestKey={initialScrollRequestKey}
      items={items}
      layoutChangeScrollBehavior={layoutChangeScrollBehavior}
      loadingLabel="Loading"
      nextBoundary={nextBoundary}
      onLoadMore={onLoadMore}
      onLoadPrevious={onLoadPrevious}
      onScrollElementChange={onScrollElementChange}
      orientation={orientation}
      previousBoundary={previousBoundary}
      previousLoadItemKey={previousLoadItemKey}
      pixelOffsetRequest={ready ? request : undefined}
      renderItem={testRenderItem}
    />
  );
}

describe("VirtualizedInfiniteList horizontal paging", () => {
  beforeEach(() => {
    virtualizer.getVirtualItems.mockReset();
    virtualizer.getOffsetForIndex.mockReset();
  });

  it("preserves a measured Workflow anchor across forward and backward window rotation", () => {
    const firstPage = Array.from({ length: 40 }, (_value, index) => `workflow-${index.toString()}`);
    const secondPage = Array.from({ length: 80 }, (_value, index) => `workflow-${index.toString()}`);
    const thirdPage = Array.from({ length: 120 }, (_value, index) => `workflow-${index.toString()}`);
    const forwardItems = thirdPage.slice(40).concat("workflow-120");
    const onLoadMore = vi.fn();
    const onLoadPrevious = vi.fn();
    virtualizer.getOffsetForIndex.mockImplementation((index: number) => {
      if (index === 31) {
        return [310];
      }
      if (index === 41) {
        return [400];
      }
      return undefined;
    });
    virtualizer.getVirtualItems.mockReturnValue([
      { end: 40, index: 0, key: "header", lane: 0, size: 40, start: 0 },
      { end: 760, index: 71, key: "workflow-70", lane: 0, size: 60, start: 700 },
      { end: 5100, index: 120, key: "workflow-119", lane: 0, size: 100, start: 5000 },
    ]);
    const view = render(
      <List
        header={<div>controls</div>}
        items={firstPage}
        onLoadMore={onLoadMore}
        orientation="horizontal"
      />,
    );
    const list = screen.getByRole("list");
    Object.defineProperty(list, "clientWidth", { configurable: true, value: 100 });

    view.rerender(<List header={<div>controls</div>} items={secondPage} orientation="horizontal" />);
    view.rerender(<List header={<div>controls</div>} items={thirdPage} orientation="horizontal" />);
    list.scrollLeft = 720;
    fireEvent.scroll(list);

    view.rerender(
      <List
        hasNextPage
        header={<div>controls</div>}
        items={thirdPage}
        onLoadMore={onLoadMore}
        orientation="horizontal"
      />,
    );
    expect(onLoadMore).toHaveBeenCalledOnce();

    virtualizer.getVirtualItems.mockReturnValue([
      { end: 40, index: 0, key: "header", lane: 0, size: 40, start: 0 },
      { end: 40, index: 1, key: "workflow-40", lane: 0, size: 40, start: 0 },
    ]);
    view.rerender(
      <List
        header={<div>controls</div>}
        items={forwardItems}
        onLoadMore={onLoadMore}
        orientation="horizontal"
      />,
    );
    expect(list.scrollLeft).toBe(330);

    virtualizer.getVirtualItems.mockReturnValue([
      { end: 40, index: 0, key: "header", lane: 0, size: 40, start: 0 },
      { end: 100, index: 1, key: "workflow-40", lane: 0, size: 60, start: 40 },
    ]);
    list.scrollLeft = 1;
    fireEvent.scroll(list);
    view.rerender(
      <List
        hasPreviousPage
        header={<div>controls</div>}
        items={forwardItems}
        onLoadPrevious={onLoadPrevious}
        orientation="horizontal"
        previousLoadItemKey="workflow-40"
      />,
    );
    expect(onLoadPrevious).toHaveBeenCalledOnce();

    virtualizer.getVirtualItems.mockReturnValue([
      { end: 40, index: 0, key: "header", lane: 0, size: 40, start: 0 },
      { end: 40, index: 1, key: "workflow-40", lane: 0, size: 40, start: 0 },
    ]);
    view.rerender(
      <List
        header={<div>controls</div>}
        items={thirdPage}
        onLoadMore={onLoadMore}
        orientation="horizontal"
      />,
    );
    expect(list.scrollLeft).toBe(361);
  });

  it("suppresses duplicate horizontal edge requests while the edge remains visible", () => {
    const onLoadMore = vi.fn();
    virtualizer.getVirtualItems.mockReturnValue([
      { end: 120, index: 2, key: "c", lane: 0, size: 40, start: 80 },
    ]);
    const view = render(
      <List
        hasNextPage
        isFetchingNextPage={false}
        items={["a", "b", "c"]}
        onLoadMore={onLoadMore}
        orientation="horizontal"
      />,
    );

    expect(onLoadMore).toHaveBeenCalledOnce();

    view.rerender(
      <List
        hasNextPage
        isFetchingNextPage
        items={["a", "b", "c"]}
        onLoadMore={onLoadMore}
        orientation="horizontal"
      />,
    );
    view.rerender(
      <List
        hasNextPage
        isFetchingNextPage={false}
        items={["a", "b", "c"]}
        onLoadMore={onLoadMore}
        orientation="horizontal"
      />,
    );

    expect(onLoadMore).toHaveBeenCalledOnce();
  });

  it("rearms both horizontal edge requests after each retained window leaves that edge", () => {
    const windowItems = (start: number, count: number) =>
      Array.from({ length: count }, (_value, index) => `workflow-${(start + index).toString()}`);
    const onLoadMore = vi.fn();
    const onLoadPrevious = vi.fn();
    const renderWindow = (
      view: ReturnType<typeof render>,
      items: readonly string[],
      visibleIndexes: readonly number[],
    ) => {
      virtualizer.getVirtualItems.mockReturnValue(
        visibleIndexes.map((index) => ({
          end: index * 10 + 10,
          index,
          key: items[index] ?? `workflow-${index.toString()}`,
          lane: 0,
          size: 10,
          start: index * 10,
        })),
      );
      view.rerender(
        <List
          hasNextPage
          hasPreviousPage
          items={items}
          onLoadMore={onLoadMore}
          onLoadPrevious={onLoadPrevious}
          orientation="horizontal"
          {...(items[0] === undefined ? {} : { previousLoadItemKey: items[0] })}
        />,
      );
    };

    virtualizer.getVirtualItems.mockReturnValue([
      { end: 1200, index: 119, key: "workflow-119", lane: 0, size: 10, start: 1190 },
    ]);
    const view = render(
      <List
        hasNextPage
        hasPreviousPage
        items={windowItems(0, 120)}
        onLoadMore={onLoadMore}
        onLoadPrevious={onLoadPrevious}
        orientation="horizontal"
        previousLoadItemKey="workflow-0"
      />,
    );
    expect(onLoadMore).toHaveBeenCalledOnce();

    const forwardItems = windowItems(40, 81);
    renderWindow(view, forwardItems, [40]);
    const initialItems = windowItems(0, 120);
    renderWindow(view, initialItems, [0]);
    expect(onLoadPrevious).toHaveBeenCalledOnce();

    renderWindow(view, initialItems, [40]);
    renderWindow(view, initialItems, [119]);
    expect(onLoadMore).toHaveBeenCalledTimes(2);

    renderWindow(view, forwardItems, [40]);
    renderWindow(view, forwardItems, [0]);
    expect(onLoadPrevious).toHaveBeenCalledTimes(2);
    renderWindow(view, initialItems, [40]);
  });
});

describe("VirtualizedInfiniteList pixel restoration", () => {
  beforeEach(() => {
    virtualizer.getVirtualItems.mockReturnValue([]);
    virtualizer.getOffsetForIndex.mockClear();
    virtualizer.scrollToIndex.mockClear();
    virtualizer.scrollToOffset.mockClear();
  });

  it("keeps explicit initial navigation when layout-change preservation is disabled", () => {
    render(
      <List
        initialScrollKey="dependencies"
        initialScrollRequestKey="task-1:dependencies"
        items={["header", "body", "dependencies"]}
        layoutChangeScrollBehavior="natural"
      />,
    );

    expect(virtualizer.scrollToIndex).toHaveBeenCalledWith(2, {
      align: "start",
      behavior: "auto",
    });
  });

  it("leaves the viewport untouched when layout-change preservation is disabled", () => {
    const initialItems = ["header", "body", "feed"];
    const refreshedItems = ["new", ...initialItems];
    virtualizer.getOffsetForIndex.mockImplementation((index: number) => [index * 40]);
    virtualizer.getVirtualItems.mockReturnValue([
      { end: 80, index: 1, key: "body", lane: 0, size: 40, start: 40 },
    ]);
    const view = render(<List items={initialItems} layoutChangeScrollBehavior="natural" />);
    const list = screen.getByRole("list");
    list.scrollTop = 40;
    fireEvent.scroll(list);

    virtualizer.getVirtualItems.mockReturnValue([
      { end: 120, index: 2, key: "body", lane: 0, size: 40, start: 80 },
    ]);
    view.rerender(<List items={refreshedItems} layoutChangeScrollBehavior="natural" />);

    expect(screen.getByRole("list").scrollTop).toBe(40);
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

  it("renders virtualized grid and row semantics", () => {
    virtualizer.getVirtualItems.mockReturnValue([
      { end: 40, index: 0, key: "header", lane: 0, size: 40, start: 0 },
      { end: 80, index: 1, key: "task", lane: 0, size: 40, start: 40 },
    ]);
    render(
      <VirtualizedInfiniteList
        estimateSize={testEstimateSize}
        getItemKey={testGetItemKey}
        hasNextPage={false}
        isFetchingNextPage={false}
        itemRole="row"
        items={["header", "task"]}
        loadingLabel="Loading"
        onLoadMore={() => undefined}
        renderItem={(item) => <div role={item === "header" ? "columnheader" : "gridcell"}>{item}</div>}
        role="grid"
        stickyItemKeys={new Set(["header"])}
      />,
    );

    expect(screen.getByRole("grid")).toBeInTheDocument();
    expect(screen.getAllByRole("row")).toHaveLength(2);
  });

  it("loads independent visible items once for their current request generation", () => {
    const loadActive = vi.fn();
    const loadDone = vi.fn();
    const view = render(
      <VirtualizedInfiniteList
        estimateSize={testEstimateSize}
        getItemKey={testGetItemKey}
        hasNextPage={false}
        isFetchingNextPage={false}
        items={["active-boundary", "done-boundary"]}
        loadingLabel="Loading"
        onLoadMore={() => undefined}
        renderItem={testRenderItem}
        visibilityTriggers={[
          {
            itemKey: "active-boundary",
            requestGeneration: "active-1",
            enabled: true,
            fetching: false,
            onVisible: loadActive,
          },
          {
            itemKey: "done-boundary",
            requestGeneration: "done-1",
            enabled: true,
            fetching: false,
            onVisible: loadDone,
          },
        ]}
      />,
    );

    expect(loadActive).toHaveBeenCalledOnce();
    expect(loadDone).toHaveBeenCalledOnce();
    view.rerender(
      <VirtualizedInfiniteList
        estimateSize={testEstimateSize}
        getItemKey={testGetItemKey}
        hasNextPage={false}
        isFetchingNextPage={false}
        items={["active-boundary", "done-boundary"]}
        loadingLabel="Loading"
        onLoadMore={() => undefined}
        renderItem={testRenderItem}
        visibilityTriggers={[
          {
            itemKey: "active-boundary",
            requestGeneration: "active-2",
            enabled: true,
            fetching: false,
            onVisible: loadActive,
          },
          {
            itemKey: "done-boundary",
            requestGeneration: "done-1",
            enabled: true,
            fetching: false,
            onVisible: loadDone,
          },
        ]}
      />,
    );
    expect(loadActive).toHaveBeenCalledTimes(2);
    expect(loadDone).toHaveBeenCalledOnce();

    view.rerender(
      <VirtualizedInfiniteList
        estimateSize={testEstimateSize}
        getItemKey={testGetItemKey}
        hasNextPage={false}
        isFetchingNextPage={false}
        items={["active-boundary", "done-boundary"]}
        loadingLabel="Loading"
        onLoadMore={() => undefined}
        renderItem={testRenderItem}
        visibilityTriggers={[
          {
            itemKey: "active-boundary",
            requestGeneration: "active-1",
            enabled: true,
            fetching: false,
            onVisible: loadActive,
          },
          {
            itemKey: "done-boundary",
            requestGeneration: "done-1",
            enabled: true,
            fetching: false,
            onVisible: loadDone,
          },
        ]}
      />,
    );
    expect(loadActive).toHaveBeenCalledTimes(3);
    expect(loadDone).toHaveBeenCalledOnce();
  });

  it("retires visibility-trigger state after its boundary is removed", () => {
    const load = vi.fn();
    const trigger = {
      itemKey: "active-boundary",
      requestGeneration: "active-1",
      enabled: true,
      fetching: false,
      onVisible: load,
    };
    const view = render(
      <VirtualizedInfiniteList
        estimateSize={testEstimateSize}
        getItemKey={testGetItemKey}
        hasNextPage={false}
        isFetchingNextPage={false}
        items={["active-boundary"]}
        loadingLabel="Loading"
        onLoadMore={() => undefined}
        renderItem={testRenderItem}
        visibilityTriggers={[trigger]}
      />,
    );
    expect(load).toHaveBeenCalledOnce();

    view.rerender(
      <VirtualizedInfiniteList
        estimateSize={testEstimateSize}
        getItemKey={testGetItemKey}
        hasNextPage={false}
        isFetchingNextPage={false}
        items={[]}
        loadingLabel="Loading"
        onLoadMore={() => undefined}
        renderItem={testRenderItem}
        visibilityTriggers={[]}
      />,
    );
    view.rerender(
      <VirtualizedInfiniteList
        estimateSize={testEstimateSize}
        getItemKey={testGetItemKey}
        hasNextPage={false}
        isFetchingNextPage={false}
        items={["active-boundary"]}
        loadingLabel="Loading"
        onLoadMore={() => undefined}
        renderItem={testRenderItem}
        visibilityTriggers={[trigger]}
      />,
    );
    expect(load).toHaveBeenCalledTimes(2);
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
