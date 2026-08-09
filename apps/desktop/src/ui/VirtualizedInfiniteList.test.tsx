import { fireEvent, render, screen } from "@testing-library/react";

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

vi.mock("@tanstack/react-virtual", () => ({
  defaultRangeExtractor: ({ startIndex, endIndex }: Readonly<{ startIndex: number; endIndex: number }>) =>
    Array.from({ length: endIndex - startIndex + 1 }, (_value, index) => startIndex + index),
  useVirtualizer: () => virtualizer,
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
}>) {
  return (
    <VirtualizedInfiniteList
      estimateSize={() => 40}
      getItemKey={(item) => item}
      hasNextPage={hasNextPage}
      hasPreviousPage={hasPreviousPage}
      isFetchingNextPage={false}
      isFetchingPreviousPage={false}
      items={items}
      loadingLabel="Loading"
      nextBoundary={nextBoundary}
      onLoadMore={onLoadMore}
      onLoadPrevious={onLoadPrevious}
      previousBoundary={previousBoundary}
      previousLoadItemKey={previousLoadItemKey}
      pixelOffsetRequest={ready ? request : undefined}
      renderItem={(item) => item}
    />
  );
}

describe("VirtualizedInfiniteList pixel restoration", () => {
  beforeEach(() => {
    virtualizer.getVirtualItems.mockReturnValue([]);
    virtualizer.scrollToOffset.mockClear();
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

  it("uses the optional first item key for newer loading and boundary placement", () => {
    const onLoadPrevious = vi.fn();
    virtualizer.getVirtualItems.mockReturnValue([
      { end: 40, index: 0, key: "a", lane: 0, size: 40, start: 0 },
    ]);
    const view = render(
      <List
        hasPreviousPage
        onLoadPrevious={onLoadPrevious}
        previousBoundary={{ state: "loading", label: "Loading" }}
        previousLoadItemKey="b"
      />,
    );
    expect(onLoadPrevious).not.toHaveBeenCalled();

    virtualizer.getVirtualItems.mockReturnValue([
      { end: 40, index: 0, key: "a", lane: 0, size: 40, start: 0 },
      { end: 80, index: 1, key: "boundary-previous", lane: 0, size: 40, start: 40 },
      { end: 120, index: 2, key: "b", lane: 0, size: 40, start: 80 },
    ]);
    view.rerender(
      <List
        hasPreviousPage
        onLoadPrevious={onLoadPrevious}
        previousBoundary={{ state: "loading", label: "Loading" }}
        previousLoadItemKey="b"
      />,
    );
    expect(onLoadPrevious).toHaveBeenCalledOnce();
    expect(screen.getByTestId("virtual-boundary-previous")).toBeInTheDocument();
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

  it("places the newer failure below fixed rows and retries without hiding them", () => {
    const onRetry = vi.fn();
    virtualizer.getVirtualItems.mockReturnValue([
      { end: 40, index: 0, key: "fixed-header", lane: 0, size: 40, start: 0 },
      { end: 80, index: 1, key: "fixed-body", lane: 0, size: 40, start: 40 },
      { end: 120, index: 2, key: "boundary-previous", lane: 0, size: 40, start: 80 },
      { end: 160, index: 3, key: "feed-1", lane: 0, size: 40, start: 120 },
    ]);
    render(
      <List
        hasPreviousPage
        items={["fixed-header", "fixed-body", "feed-1"]}
        previousBoundary={{
          state: "error",
          message: "page failed",
          onRetry,
          retryLabel: "Retry",
        }}
        previousLoadItemKey="feed-1"
        onLoadPrevious={vi.fn()}
      />,
    );

    expect(screen.getByText("fixed-header")).toBeInTheDocument();
    expect(screen.getByText("fixed-body")).toBeInTheDocument();
    expect(screen.getByText("feed-1")).toBeInTheDocument();
    expect(screen.getByTestId("virtual-boundary-previous")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(onRetry).toHaveBeenCalledOnce();
  });
});
