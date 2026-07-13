import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { VirtualizedInfiniteList } from "./VirtualizedInfiniteList";
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
  wasFetchingNextPage: false,
};

describe("resolveLoadMore", () => {
  it("requests the next page when scrolled to the bottom for an unseen key", () => {
    expect(resolveLoadMore({ ...atBottom, lastLoadMoreKey: "", loadMoreKey: "page-1" })).toEqual({
      shouldLoad: true,
      lastLoadMoreKey: "page-1",
    });
  });

  it("does not re-request the key that is already in flight", () => {
    expect(
      resolveLoadMore({
        ...atBottom,
        isFetchingNextPage: true,
        lastLoadMoreKey: "page-1",
        loadMoreKey: "page-1",
      }),
    ).toEqual({ shouldLoad: false, lastLoadMoreKey: "page-1" });
  });

  it("releases suppression when a fetch settles without advancing the key", () => {
    // A failed/canceled fetch leaves loadMoreKey unchanged; the suppression must
    // be cleared so a later scroll can retry the same page.
    expect(
      resolveLoadMore({
        ...atBottom,
        wasFetchingNextPage: true,
        lastLoadMoreKey: "page-1",
        loadMoreKey: "page-1",
      }),
    ).toEqual({ shouldLoad: false, lastLoadMoreKey: "" });
  });

  it("retries the failed page on the next pass after suppression was released", () => {
    expect(resolveLoadMore({ ...atBottom, lastLoadMoreKey: "", loadMoreKey: "page-1" })).toEqual({
      shouldLoad: true,
      lastLoadMoreKey: "page-1",
    });
  });

  it("does not re-request a key that already advanced after a successful fetch", () => {
    expect(
      resolveLoadMore({
        ...atBottom,
        wasFetchingNextPage: true,
        lastLoadMoreKey: "page-1",
        loadMoreKey: "page-2",
      }),
    ).toEqual({ shouldLoad: true, lastLoadMoreKey: "page-2" });
  });

  it("does not request more while away from the bottom", () => {
    expect(
      resolveLoadMore({ ...atBottom, atBottom: false, lastLoadMoreKey: "", loadMoreKey: "page-1" }),
    ).toEqual({ shouldLoad: false, lastLoadMoreKey: "" });
  });

  it("keeps previous and next retry suppression independent", () => {
    const previous = resolveLoadMore({
      ...atBottom,
      wasFetchingNextPage: true,
      lastLoadMoreKey: "previous-1",
      loadMoreKey: "previous-1",
    });
    const next = resolveLoadMore({
      ...atBottom,
      isFetchingNextPage: true,
      lastLoadMoreKey: "next-1",
      loadMoreKey: "next-1",
    });

    expect(previous).toEqual({ shouldLoad: false, lastLoadMoreKey: "" });
    expect(next).toEqual({ shouldLoad: false, lastLoadMoreKey: "next-1" });
  });
});

describe("virtualizedInitialScrollIndex", () => {
  const items = [{ key: "header" }, { key: "inbox" }, { key: "activity" }];
  const getItemKey = (item: (typeof items)[number]): string => item.key;

  it("finds the target item after the virtual list header", () => {
    expect(
      virtualizedInitialScrollIndex({
        getItemKey,
        headerCount: 1,
        initialScrollKey: "inbox",
        items,
      }),
    ).toBe(2);
  });

  it("ignores missing or empty initial scroll keys", () => {
    expect(
      virtualizedInitialScrollIndex({
        getItemKey,
        headerCount: 1,
        initialScrollKey: "",
        items,
      }),
    ).toBeNull();
    expect(
      virtualizedInitialScrollIndex({
        getItemKey,
        headerCount: 1,
        initialScrollKey: "missing",
        items,
      }),
    ).toBeNull();
  });

  it("suppresses repeated request keys while allowing the same target for a new request", () => {
    expect(
      resolveVirtualizedInitialScroll({
        getItemKey,
        headerCount: 1,
        initialScrollKey: "inbox",
        initialScrollRequestKey: "task-1",
        items,
        lastRequestKey: "task-1",
      }),
    ).toBeNull();
    expect(
      resolveVirtualizedInitialScroll({
        getItemKey,
        headerCount: 1,
        initialScrollKey: "inbox",
        initialScrollRequestKey: "task-2",
        items,
        lastRequestKey: "task-1",
      }),
    ).toEqual({ requestKey: "task-2", scrollIndex: 2 });
  });
});

describe("shouldAdjustScrollForVirtualizedResize", () => {
  it("preserves a keyed row's viewport anchor while retaining normal compensation for other rows", () => {
    expect(shouldAdjustScrollForVirtualizedResize("body", "body")).toBe(false);
    expect(shouldAdjustScrollForVirtualizedResize("body", "header")).toBe(true);
  });
});

describe("VirtualizedInfiniteList bidirectional boundaries", () => {
  it("loads independently from the visible top and bottom boundaries", async () => {
    const onLoadPrevious = vi.fn();
    const onLoadNext = vi.fn();
    const props = {
      ...virtualListProps([{ id: "item-1" }]),
      hasNextPage: true,
      onLoadMore: onLoadNext,
      hasPreviousPage: true,
      isFetchingPreviousPage: false,
      previousLoadKey: "previous-1",
      onLoadPrevious,
    };

    render(<VirtualizedInfiniteList {...props} />);

    await waitFor(() => {
      expect(onLoadPrevious).toHaveBeenCalledTimes(1);
    });
    expect(onLoadNext).toHaveBeenCalledTimes(1);
  });

  it("keeps directional boundary slots mounted across idle, loading, and error states", () => {
    const previousRetry = vi.fn();
    const nextRetry = vi.fn();
    const idleProps = {
      ...virtualListProps([{ id: "item-1" }]),
      previousBoundary: { state: "idle" } as const,
      nextBoundary: { state: "idle" } as const,
    };
    const view = render(<VirtualizedInfiniteList {...idleProps} />);
    const previousSlot = screen.getByTestId("virtual-boundary-previous");
    const nextSlot = screen.getByTestId("virtual-boundary-next");

    view.rerender(
      <VirtualizedInfiniteList
        {...{
          ...idleProps,
          previousBoundary: { state: "loading", label: "Loading newer cards" } as const,
          nextBoundary: { state: "loading", label: "Loading older cards" } as const,
        }}
      />,
    );
    const loadingPreviousSlot = screen.getByTestId("virtual-boundary-previous");
    const loadingNextSlot = screen.getByTestId("virtual-boundary-next");
    expect(loadingPreviousSlot).toBe(previousSlot);
    expect(loadingNextSlot).toBe(nextSlot);
    expect(within(loadingPreviousSlot).getByRole("status")).toHaveAccessibleName("Loading newer cards");
    expect(within(loadingNextSlot).getByRole("status")).toHaveAccessibleName("Loading older cards");

    view.rerender(
      <VirtualizedInfiniteList
        {...{
          ...idleProps,
          previousBoundary: {
            state: "error",
            message: "Newer cards failed",
            retryLabel: "Retry newer cards",
            onRetry: previousRetry,
          } as const,
          nextBoundary: {
            state: "error",
            message: "Older cards failed",
            retryLabel: "Retry older cards",
            onRetry: nextRetry,
          } as const,
        }}
      />,
    );
    expect(screen.getByTestId("virtual-boundary-previous")).toBe(previousSlot);
    expect(screen.getByTestId("virtual-boundary-next")).toBe(nextSlot);
    fireEvent.click(screen.getByRole("button", { name: "Retry newer cards" }));
    fireEvent.click(screen.getByRole("button", { name: "Retry older cards" }));
    expect(previousRetry).toHaveBeenCalledTimes(1);
    expect(nextRetry).toHaveBeenCalledTimes(1);
  });

  it("registers the production scroll element and clears it on unmount", async () => {
    const onScrollElementChange = vi.fn();
    const props = {
      ...virtualListProps([{ id: "item-1" }]),
      onScrollElementChange,
    };
    const view = render(<VirtualizedInfiniteList {...props} />);

    await waitFor(() => {
      expect(onScrollElementChange).toHaveBeenCalledWith(expect.any(HTMLDivElement));
    });
    view.unmount();
    expect(onScrollElementChange).toHaveBeenLastCalledWith(null);
  });

  it("preserves the leading item and in-row offset when rows are prepended", async () => {
    const initialItems = Array.from({ length: 20 }, (_value, index) => ({
      id: `item-${index.toString()}`,
    }));
    const props = virtualListProps(initialItems);
    const view = render(<VirtualizedInfiniteList {...props} />);
    const scrollElement = screen.getByTestId("virtual-list");
    scrollElement.scrollTop = 150;
    fireEvent.scroll(scrollElement);

    view.rerender(
      <VirtualizedInfiniteList
        {...virtualListProps([{ id: "prepended-0" }, { id: "prepended-1" }, ...initialItems])}
      />,
    );

    await waitFor(() => {
      expect(scrollElement.scrollTop).toBe(350);
    });
  });

  it("keeps pinned item keys mounted beyond overscan and releases them after clearing the pin", () => {
    const items = Array.from({ length: 100 }, (_value, index) => ({
      id: `item-${index.toString()}`,
    }));
    const props = {
      ...virtualListProps(items),
      pinnedItemKeys: new Set(["item-99"]),
    };
    const view = render(<VirtualizedInfiniteList {...props} />);
    expect(screen.getByText("item-99")).toBeInTheDocument();

    view.rerender(
      <VirtualizedInfiniteList
        {...{
          ...props,
          pinnedItemKeys: new Set<string>(),
        }}
      />,
    );
    expect(screen.queryByText("item-99")).not.toBeInTheDocument();
  });
});

function virtualListProps(items: readonly Readonly<{ id: string }>[]) {
  return {
    items,
    getItemKey: (item: Readonly<{ id: string }>) => item.id,
    renderItem: (item: Readonly<{ id: string }>) => <span>{item.id}</span>,
    hasNextPage: false,
    isFetchingNextPage: false,
    loadingLabel: "Loading items",
    onLoadMore: vi.fn(),
    estimateSize: () => 100,
    testId: "virtual-list",
  };
}
