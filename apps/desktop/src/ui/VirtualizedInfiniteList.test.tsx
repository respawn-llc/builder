import { describe, expect, it } from "vitest";

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
