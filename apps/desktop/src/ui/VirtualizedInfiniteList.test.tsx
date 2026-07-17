import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { VirtualizedInfiniteList, type VirtualizedInfiniteListProps } from "./VirtualizedInfiniteList";
import {
  resolveVirtualizedInitialScroll,
  virtualizedInitialScrollIndex,
} from "./virtualizedInfiniteListInitialScroll";
import { resolveLoadMore } from "./virtualizedInfiniteListLoadMore";
import { shouldAdjustScrollForVirtualizedResize } from "./virtualizedResizePolicy";

const atBottom = {
  atBottom: true,
  hasNextPage: true,
  isFetchingNextPage: false,
  lastLoadMoreKey: null,
  loadMoreKey: "page-1",
  wasFetchingNextPage: false,
} satisfies Parameters<typeof resolveLoadMore>[0];

function resolveAtBottom(overrides: Partial<Parameters<typeof resolveLoadMore>[0]> = {}) {
  return resolveLoadMore({ ...atBottom, ...overrides });
}

describe("resolveLoadMore", () => {
  it("requests the next page when scrolled to the bottom for an unseen key", () => {
    expect(resolveAtBottom()).toEqual({
      shouldLoad: true,
      lastLoadMoreKey: "page-1",
    });
  });

  it("does not re-request the key that is already in flight", () => {
    expect(
      resolveAtBottom({
        isFetchingNextPage: true,
        lastLoadMoreKey: "page-1",
      }),
    ).toEqual({ shouldLoad: false, lastLoadMoreKey: "page-1" });
  });

  it("releases suppression when a fetch settles without advancing the key", () => {
    // A failed/canceled fetch leaves loadMoreKey unchanged; the suppression must
    // be cleared so a later scroll can retry the same page.
    expect(
      resolveAtBottom({
        wasFetchingNextPage: true,
        lastLoadMoreKey: "page-1",
      }),
    ).toEqual({ shouldLoad: false, lastLoadMoreKey: null });
  });

  it("retries the failed page on the next pass after suppression was released", () => {
    expect(resolveAtBottom()).toEqual({
      shouldLoad: true,
      lastLoadMoreKey: "page-1",
    });
  });

  it("does not re-request a key that already advanced after a successful fetch", () => {
    expect(
      resolveAtBottom({
        wasFetchingNextPage: true,
        lastLoadMoreKey: "page-1",
        loadMoreKey: "page-2",
      }),
    ).toEqual({ shouldLoad: true, lastLoadMoreKey: "page-2" });
  });

  it("does not request more while away from the bottom", () => {
    expect(resolveAtBottom({ atBottom: false })).toEqual({ shouldLoad: false, lastLoadMoreKey: null });
  });

  it("keeps previous and next retry suppression independent", () => {
    const previous = resolveAtBottom({
      wasFetchingNextPage: true,
      lastLoadMoreKey: "previous-1",
      loadMoreKey: "previous-1",
    });
    const next = resolveAtBottom({
      isFetchingNextPage: true,
      lastLoadMoreKey: "next-1",
      loadMoreKey: "next-1",
    });

    expect(previous).toEqual({ shouldLoad: false, lastLoadMoreKey: null });
    expect(next).toEqual({ shouldLoad: false, lastLoadMoreKey: "next-1" });
  });
});

describe("virtualizedInitialScrollIndex", () => {
  const items = [{ key: "header" }, { key: "inbox" }, { key: "activity" }];
  const getItemKey = (item: (typeof items)[number]): string => item.key;

  function scrollIndex(initialScrollKey: string | undefined): number | null {
    return virtualizedInitialScrollIndex({
      getItemKey,
      headerCount: 1,
      initialScrollKey,
      items,
    });
  }

  function resolvedScroll(initialScrollRequestKey: string, lastRequestKey: string | null) {
    return resolveVirtualizedInitialScroll({
      getItemKey,
      headerCount: 1,
      initialScrollKey: "inbox",
      initialScrollRequestKey,
      items,
      lastRequestKey,
    });
  }

  it("finds the target item after the virtual list header", () => {
    expect(scrollIndex("inbox")).toBe(2);
  });

  it("ignores absent or unmatched initial scroll keys", () => {
    expect(scrollIndex(undefined)).toBeNull();
    expect(scrollIndex("missing")).toBeNull();
  });

  it("suppresses repeated request keys while allowing the same target for a new request", () => {
    expect(resolvedScroll("task-1", null)).toEqual({ requestKey: "task-1", scrollIndex: 2 });
    expect(resolvedScroll("task-1", "task-1")).toBeNull();
    expect(resolvedScroll("task-2", "task-1")).toEqual({ requestKey: "task-2", scrollIndex: 2 });
  });
});

describe("shouldAdjustScrollForVirtualizedResize", () => {
  it("preserves a keyed row's viewport anchor while retaining normal compensation for other rows", () => {
    expect(shouldAdjustScrollForVirtualizedResize("body", "body")).toBe(false);
    expect(shouldAdjustScrollForVirtualizedResize("body", "header")).toBe(true);
  });
});

describe("VirtualizedInfiniteList bidirectional boundaries", () => {
  it("passes item indexes to renderers without counting virtual chrome rows", () => {
    const items = [{ id: "item-1" }, { id: "item-2" }];

    renderVirtualList(items, {
      header: <span>Header</span>,
      renderItem: (item, itemIndex) => (
        <span>
          {itemIndex}:{item.id}
        </span>
      ),
    });

    expect(screen.getByText("0:item-1")).toBeInTheDocument();
    expect(screen.getByText("1:item-2")).toBeInTheDocument();
  });

  it("loads independently from the visible top and bottom boundaries", async () => {
    const onLoadPrevious = vi.fn();
    const onLoadNext = vi.fn();
    renderVirtualList([{ id: "item-1" }], {
      hasNextPage: true,
      onLoadMore: onLoadNext,
      hasPreviousPage: true,
      isFetchingPreviousPage: false,
      previousLoadKey: "previous-1",
      onLoadPrevious,
    });

    await waitFor(() => {
      expect(onLoadPrevious).toHaveBeenCalledTimes(1);
    });
    expect(onLoadNext).toHaveBeenCalledTimes(1);
  });

  it("does not reserve directional boundary rows unless they represent loading or error", () => {
    const previousRetry = vi.fn();
    const nextRetry = vi.fn();
    const view = renderVirtualList([{ id: "item-1" }]);
    expect(screen.queryByTestId("virtual-boundary-previous")).not.toBeInTheDocument();
    expect(screen.queryByTestId("virtual-boundary-next")).not.toBeInTheDocument();
    expect(screen.getAllByRole("listitem")).toHaveLength(1);

    view.rerender({
      previousBoundary: { state: "loading", label: "Loading newer cards" },
      nextBoundary: { state: "loading", label: "Loading older cards" },
    });
    const loadingPreviousSlot = screen.getByTestId("virtual-boundary-previous");
    const loadingNextSlot = screen.getByTestId("virtual-boundary-next");
    expect(within(loadingPreviousSlot).getByRole("status")).toHaveAccessibleName("Loading newer cards");
    expect(within(loadingNextSlot).getByRole("status")).toHaveAccessibleName("Loading older cards");

    view.rerender({
      previousBoundary: {
        state: "error",
        message: "Newer cards failed",
        retryLabel: "Retry newer cards",
        onRetry: previousRetry,
      },
      nextBoundary: {
        state: "error",
        message: "Older cards failed",
        retryLabel: "Retry older cards",
        onRetry: nextRetry,
      },
    });
    fireEvent.click(screen.getByRole("button", { name: "Retry newer cards" }));
    fireEvent.click(screen.getByRole("button", { name: "Retry older cards" }));
    expect(previousRetry).toHaveBeenCalledTimes(1);
    expect(nextRetry).toHaveBeenCalledTimes(1);
  });

  it("registers the production scroll element and clears it on unmount", async () => {
    const onScrollElementChange = vi.fn();
    const view = renderVirtualList([{ id: "item-1" }], {
      onScrollElementChange,
    });

    await waitFor(() => {
      expect(onScrollElementChange.mock.calls[0]?.[0]).toBeInstanceOf(HTMLDivElement);
    });
    view.unmount();
    expect(onScrollElementChange).toHaveBeenLastCalledWith(null);
  });

  it("preserves the leading item and in-row offset when rows are prepended", async () => {
    const initialItems = Array.from({ length: 20 }, (_value, index) => ({
      id: `item-${index.toString()}`,
    }));
    const view = renderVirtualList(initialItems);
    const scrollElement = screen.getByTestId("virtual-list");
    scrollElement.scrollTop = 150;
    fireEvent.scroll(scrollElement);

    view.rerender({ items: [{ id: "prepended-0" }, { id: "prepended-1" }, ...initialItems] });

    await waitFor(() => {
      expect(scrollElement.scrollTop).toBe(350);
    });
  });

  it("keeps pinned item keys mounted beyond overscan and releases them after clearing the pin", () => {
    const items = Array.from({ length: 100 }, (_value, index) => ({
      id: `item-${index.toString()}`,
    }));
    const view = renderVirtualList(items, {
      pinnedItemKeys: new Set(["item-99"]),
    });
    expect(screen.getByText("item-99")).toBeInTheDocument();

    view.rerender({ pinnedItemKeys: new Set<string>() });
    expect(screen.queryByText("item-99")).not.toBeInTheDocument();
  });
});

type TestItem = Readonly<{ id: string }>;
type TestListProps = VirtualizedInfiniteListProps<TestItem>;

function virtualListProps(items: readonly TestItem[]): TestListProps {
  return {
    items,
    getItemKey: (item) => item.id,
    renderItem: (item) => <span>{item.id}</span>,
    hasNextPage: false,
    isFetchingNextPage: false,
    loadingLabel: "Loading items",
    onLoadMore: vi.fn(),
    estimateSize: () => 100,
    testId: "virtual-list",
  };
}

function renderVirtualList(items: readonly TestItem[], overrides: Partial<TestListProps> = {}) {
  const props = { ...virtualListProps(items), ...overrides };
  const view = render(<VirtualizedInfiniteList {...props} />);
  return {
    ...view,
    rerender: (next: Partial<TestListProps>) => {
      view.rerender(<VirtualizedInfiniteList {...props} {...next} />);
    },
  };
}
