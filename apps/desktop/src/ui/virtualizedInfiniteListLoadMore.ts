import { useEffect } from "react";

export type LoadMoreDecision = Readonly<{
  shouldLoad: boolean;
  lastLoadMoreKey: string | null;
}>;

// resolveLoadMore decides whether to request the next page. loadMoreKey only
// advances when a fetch succeeds, so a fetch that settles without advancing it
// (failed or canceled) releases the suppression — otherwise the same key would
// stay permanently blocked and scrolling could never retry the failed page.
export function resolveLoadMore({
  atBottom,
  hasNextPage,
  isFetchingNextPage,
  lastLoadMoreKey,
  loadMoreKey,
  wasFetchingNextPage,
}: Readonly<{
  atBottom: boolean;
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  lastLoadMoreKey: string | null;
  loadMoreKey: string;
  wasFetchingNextPage: boolean;
}>): LoadMoreDecision {
  if (wasFetchingNextPage && !isFetchingNextPage && lastLoadMoreKey === loadMoreKey) {
    return { shouldLoad: false, lastLoadMoreKey: null };
  }
  if (atBottom && hasNextPage && !isFetchingNextPage && lastLoadMoreKey !== loadMoreKey) {
    return { shouldLoad: true, lastLoadMoreKey: loadMoreKey };
  }
  return { shouldLoad: false, lastLoadMoreKey };
}

export function resolvePreviousLoadEdge<TItem>({
  getItemKey,
  itemStartIndex,
  items,
  previousLoadItemKey,
  visibleIndexes,
}: Readonly<{
  getItemKey: (item: TItem) => string;
  itemStartIndex: number;
  items: readonly TItem[];
  previousLoadItemKey: string | undefined;
  visibleIndexes: readonly number[];
}>): boolean {
  const firstVisibleIndex = visibleIndexes[0];
  if (previousLoadItemKey === undefined) {
    return firstVisibleIndex !== undefined && firstVisibleIndex <= itemStartIndex;
  }
  return visibleIndexes.some((virtualIndex) => {
    const item =
      virtualIndex >= itemStartIndex && virtualIndex < itemStartIndex + items.length
        ? items[virtualIndex - itemStartIndex]
        : undefined;
    return item !== undefined && getItemKey(item) === previousLoadItemKey;
  });
}

export function resolveNextLoadEdge(
  itemStartIndex: number,
  itemCount: number,
  visibleIndexes: readonly number[],
): boolean {
  const lastVisibleIndex = visibleIndexes.at(-1);
  const lastDataIndex = itemStartIndex + itemCount - 1;
  return lastVisibleIndex !== undefined && lastVisibleIndex >= lastDataIndex;
}

export function useVirtualizedLoadMore({
  atEdge,
  hasNextPage,
  isFetchingNextPage,
  lastLoadMoreKeyRef,
  loadMoreKey,
  onLoadMore,
  wasFetchingNextPageRef,
}: Readonly<{
  atEdge: boolean;
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  lastLoadMoreKeyRef: { current: string | null };
  loadMoreKey: string;
  onLoadMore: (() => void) | undefined;
  wasFetchingNextPageRef: { current: boolean };
}>): void {
  useEffect(() => {
    if (onLoadMore === undefined) {
      return;
    }
    const decision = resolveLoadMore({
      atBottom: atEdge,
      hasNextPage,
      isFetchingNextPage,
      lastLoadMoreKey: lastLoadMoreKeyRef.current,
      loadMoreKey,
      wasFetchingNextPage: wasFetchingNextPageRef.current,
    });
    wasFetchingNextPageRef.current = isFetchingNextPage;
    lastLoadMoreKeyRef.current = decision.lastLoadMoreKey;
    if (decision.shouldLoad) {
      onLoadMore();
    }
  }, [
    atEdge,
    hasNextPage,
    isFetchingNextPage,
    lastLoadMoreKeyRef,
    loadMoreKey,
    onLoadMore,
    wasFetchingNextPageRef,
  ]);
}
