import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, type ReactNode } from "react";
import { type Range, type VirtualItem, useVirtualizer } from "@tanstack/react-virtual";

import { cx } from "./classes";
import {
  InfiniteListBoundary,
  type VirtualizedInfiniteListBoundaryState,
} from "./InfiniteListBoundary";
import { Spinner } from "./Spinner";
import { resolveVirtualizedInitialScroll } from "./virtualizedInfiniteListInitialScroll";
import { resolveLoadMore } from "./virtualizedInfiniteListLoadMore";
import { pinnedVirtualRangeExtractor } from "./virtualizedPinnedRange";
import { shouldAdjustScrollForVirtualizedResize } from "./virtualizedResizePolicy";

export type { VirtualizedInfiniteListBoundaryState } from "./InfiniteListBoundary";

export type VirtualizedInfiniteListProps<TItem> = Readonly<{
  items: readonly TItem[];
  getItemKey: (item: TItem) => string;
  renderItem: (item: TItem) => ReactNode;
  header?: ReactNode | undefined;
  empty?: ReactNode | undefined;
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  loadingLabel: string;
  loadMoreKey?: string | undefined;
  onLoadMore: () => void;
  estimateSize: () => number;
  ariaLabel?: string | undefined;
  rowSpacing?: "default" | "compact" | undefined;
  testId?: string | undefined;
  initialScrollKey?: string | undefined;
  initialScrollRequestKey?: string | undefined;
  paddingEnd?: number | undefined;
  paddingStart?: number | undefined;
  className?: string | undefined;
  nonAdjustingResizeItemKey?: string | undefined;
  hasPreviousPage?: boolean | undefined;
  isFetchingPreviousPage?: boolean | undefined;
  previousLoadKey?: string | undefined;
  onLoadPrevious?: (() => void) | undefined;
  previousBoundary?: VirtualizedInfiniteListBoundaryState | undefined;
  nextBoundary?: VirtualizedInfiniteListBoundaryState | undefined;
  onScrollElementChange?: ((element: HTMLDivElement | null) => void) | undefined;
  pinnedItemKeys?: ReadonlySet<string> | undefined;
}>;

export function VirtualizedInfiniteList<TItem>({
  items,
  getItemKey,
  renderItem,
  header,
  empty,
  hasNextPage,
  isFetchingNextPage,
  loadingLabel,
  loadMoreKey,
  onLoadMore,
  estimateSize,
  ariaLabel,
  rowSpacing = "default",
  testId,
  initialScrollKey,
  initialScrollRequestKey,
  paddingEnd = 0,
  paddingStart = 0,
  className,
  nonAdjustingResizeItemKey,
  hasPreviousPage = false,
  isFetchingPreviousPage = false,
  previousLoadKey,
  onLoadPrevious,
  previousBoundary,
  nextBoundary,
  onScrollElementChange,
  pinnedItemKeys,
}: VirtualizedInfiniteListProps<TItem>) {
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const setScrollElement = useCallback(
    (element: HTMLDivElement | null) => {
      scrollRef.current = element;
      onScrollElementChange?.(element);
    },
    [onScrollElementChange],
  );
  const lastInitialScrollKeyRef = useRef("");
  const lastLoadPreviousKeyRef = useRef("");
  const lastLoadMoreKeyRef = useRef("");
  const wasFetchingPreviousPageRef = useRef(false);
  const wasFetchingNextPageRef = useRef(false);
  const leadingAnchorRef = useRef<VirtualizedLeadingAnchor | null>(null);
  const previousItemKeysRef = useRef<readonly string[]>([]);
  const {
    count,
    emptyCount,
    headerCount,
    itemStartIndex,
    legacyPlaceholderIndex,
    nextBoundaryIndex,
    previousBoundaryCount,
  } = virtualizedListLayout({
    empty,
    hasHeader: header !== undefined,
    hasNextPage,
    itemCount: items.length,
    nextBoundary,
    previousBoundary,
  });
  const pinnedIndexes = useMemo(() => {
    if (pinnedItemKeys === undefined || pinnedItemKeys.size === 0) {
      return new Set<number>();
    }
    const indexes = new Set<number>();
    items.forEach((item, index) => {
      if (pinnedItemKeys.has(getItemKey(item))) {
        indexes.add(itemStartIndex + index);
      }
    });
    return indexes;
  }, [getItemKey, itemStartIndex, items, pinnedItemKeys]);
  // TanStack Virtual is the intended windowing boundary; returned instance methods are not passed to memoized children.
  // The react-hooks/incompatible-library check is scoped off for this file in eslint.config.js.
  const virtualizer = useVirtualizer({
    count,
    getScrollElement: () => scrollRef.current,
    estimateSize,
    initialRect: { width: 800, height: 600 },
    paddingEnd,
    paddingStart,
    getItemKey: (index) => {
      if (previousBoundary !== undefined && index === 0) {
        return "boundary-previous";
      }
      if (header !== undefined && index === previousBoundaryCount) {
        return "header";
      }
      if (items.length === 0 && empty !== undefined && index === itemStartIndex) {
        return "empty";
      }
      const item = items[index - itemStartIndex];
      if (item !== undefined) {
        return getItemKey(item);
      }
      if (nextBoundaryIndex === index) {
        return "boundary-next";
      }
      return `placeholder-${index.toString()}`;
    },
    overscan: 6,
    rangeExtractor: (range) => pinnedVirtualRangeExtractor(range, pinnedIndexes),
  });
  virtualizer.shouldAdjustScrollPositionOnItemSizeChange =
    nonAdjustingResizeItemKey === undefined
      ? undefined
      : (item: VirtualItem) =>
          shouldAdjustScrollForVirtualizedResize(nonAdjustingResizeItemKey, String(item.key));
  const virtualItems = virtualizer.getVirtualItems();
  const isFallbackRendering = virtualItems.length === 0;
  const fallbackIndexes = fallbackVirtualIndexes({
    count,
    estimateSize,
    pinnedIndexes,
  });
  const visibleIndexes = isFallbackRendering
    ? fallbackIndexes
    : virtualItems.map((virtualItem) => virtualItem.index);
  const renderRow = (virtualIndex: number): ReactNode =>
    renderVirtualRow({
      empty,
      emptyCount,
      header,
      itemStartIndex,
      isFetchingNextPage,
      item: items[virtualIndex - itemStartIndex],
      legacyPlaceholderIndex,
      loadingLabel,
      nextBoundary,
      nextBoundaryIndex,
      previousBoundary,
      renderItem,
      virtualIndex,
    });

  const captureLeadingAnchor = useCallback(() => {
    const element = scrollRef.current;
    if (element === null || items.length === 0) {
      leadingAnchorRef.current = null;
      return;
    }
    const scrollTop = element.scrollTop;
    const virtualItem = virtualizer
      .getVirtualItems()
      .find(
        (item) =>
          item.index >= itemStartIndex && item.index < itemStartIndex + items.length && item.end > scrollTop,
      );
    if (virtualItem !== undefined) {
      const item = items[virtualItem.index - itemStartIndex];
      leadingAnchorRef.current =
        item === undefined ? null : { itemKey: getItemKey(item), inRowOffset: scrollTop - virtualItem.start };
      return;
    }
    const estimatedSize = Math.max(1, estimateSize());
    const estimatedVirtualIndex = Math.max(
      itemStartIndex,
      Math.floor(Math.max(0, scrollTop - paddingStart) / estimatedSize),
    );
    const dataIndex = Math.min(items.length - 1, estimatedVirtualIndex - itemStartIndex);
    const item = items[dataIndex];
    leadingAnchorRef.current =
      item === undefined
        ? null
        : {
            itemKey: getItemKey(item),
            inRowOffset: scrollTop - (paddingStart + estimatedVirtualIndex * estimatedSize),
          };
  }, [estimateSize, getItemKey, itemStartIndex, items, paddingStart, virtualizer]);

  useEffect(() => {
    if (initialScrollKey === undefined || initialScrollKey.length === 0) {
      return;
    }
    const scroll = resolveVirtualizedInitialScroll({
      getItemKey,
      headerCount: itemStartIndex,
      initialScrollKey,
      initialScrollRequestKey,
      items,
      lastRequestKey: lastInitialScrollKeyRef.current,
    });
    if (scroll === null) {
      return;
    }
    lastInitialScrollKeyRef.current = scroll.requestKey;
    virtualizer.scrollToIndex(scroll.scrollIndex, { align: "start", behavior: "auto" });
  }, [getItemKey, initialScrollKey, initialScrollRequestKey, itemStartIndex, items, virtualizer]);

  useLayoutEffect(() => {
    const currentKeys = items.map(getItemKey);
    const previousKeys = previousItemKeysRef.current;
    const anchor = leadingAnchorRef.current;
    const element = scrollRef.current;
    if (previousKeys.length > 0 && anchor !== null && element !== null) {
      const previousIndex = previousKeys.indexOf(anchor.itemKey);
      const currentIndex = currentKeys.indexOf(anchor.itemKey);
      if (previousIndex >= 0 && currentIndex >= 0 && previousIndex !== currentIndex) {
        const virtualIndex = itemStartIndex + currentIndex;
        const measuredOffset = isFallbackRendering
          ? undefined
          : virtualizer.getOffsetForIndex(virtualIndex, "start")?.[0];
        const rowOffset = measuredOffset ?? paddingStart + virtualIndex * Math.max(1, estimateSize());
        const scrollOffset = rowOffset + anchor.inRowOffset;
        element.scrollTop = scrollOffset;
        if (!isFallbackRendering) {
          virtualizer.scrollToOffset(scrollOffset, { behavior: "auto" });
        }
      }
    }
    previousItemKeysRef.current = currentKeys;
    captureLeadingAnchor();
  }, [
    captureLeadingAnchor,
    estimateSize,
    getItemKey,
    isFallbackRendering,
    itemStartIndex,
    items,
    paddingStart,
    virtualizer,
  ]);

  useEffect(() => {
    if (onLoadPrevious === undefined) {
      return;
    }
    const firstVisibleIndex = visibleIndexes[0];
    const decision = resolveLoadMore({
      atBottom: firstVisibleIndex !== undefined && firstVisibleIndex <= itemStartIndex,
      hasNextPage: hasPreviousPage,
      isFetchingNextPage: isFetchingPreviousPage,
      lastLoadMoreKey: lastLoadPreviousKeyRef.current,
      loadMoreKey: previousLoadKey ?? items.length.toString(),
      wasFetchingNextPage: wasFetchingPreviousPageRef.current,
    });
    wasFetchingPreviousPageRef.current = isFetchingPreviousPage;
    lastLoadPreviousKeyRef.current = decision.lastLoadMoreKey;
    if (decision.shouldLoad) {
      onLoadPrevious();
    }
  }, [
    hasPreviousPage,
    isFetchingPreviousPage,
    itemStartIndex,
    items.length,
    onLoadPrevious,
    previousLoadKey,
    visibleIndexes,
  ]);

  useEffect(() => {
    const lastVisibleIndex = visibleIndexes.at(-1);
    const lastDataIndex = itemStartIndex + items.length - 1;
    const decision = resolveLoadMore({
      atBottom: lastVisibleIndex !== undefined && lastVisibleIndex >= lastDataIndex,
      hasNextPage,
      isFetchingNextPage,
      lastLoadMoreKey: lastLoadMoreKeyRef.current,
      loadMoreKey: loadMoreKey ?? items.length.toString(),
      wasFetchingNextPage: wasFetchingNextPageRef.current,
    });
    wasFetchingNextPageRef.current = isFetchingNextPage;
    lastLoadMoreKeyRef.current = decision.lastLoadMoreKey;
    if (decision.shouldLoad) {
      onLoadMore();
    }
  }, [
    hasNextPage,
    isFetchingNextPage,
    itemStartIndex,
    items.length,
    loadMoreKey,
    onLoadMore,
    visibleIndexes,
  ]);

  if (count > 0 && virtualItems.length === 0) {
    return (
      <div
        aria-label={ariaLabel}
        className={className}
        data-testid={testId}
        onScroll={captureLeadingAnchor}
        ref={setScrollElement}
        role="list"
      >
        {fallbackIndexes.map((index) => (
          <div
            className={virtualRowClassName({ count, index, rowSpacing, virtualized: false })}
            key={fallbackRowKey({
              emptyCount,
              getItemKey,
              headerCount,
              index,
              itemStartIndex,
              items,
              nextBoundaryIndex,
              previousBoundaryCount,
            })}
            role="listitem"
            style={fallbackRowStyle({ count, index, paddingEnd, paddingStart })}
          >
            {renderRow(index)}
          </div>
        ))}
      </div>
    );
  }

  return (
    <div
      aria-label={ariaLabel}
      className={className}
      data-testid={testId}
      onScroll={captureLeadingAnchor}
      ref={setScrollElement}
      role="list"
    >
      <div className="relative w-full" style={{ height: `${virtualizer.getTotalSize().toString()}px` }}>
        {virtualItems.map((virtualItem) => {
          return (
            <div
              className={cx(
                "absolute top-0 left-0 w-full",
                virtualRowClassName({ count, index: virtualItem.index, rowSpacing, virtualized: true }),
              )}
              data-index={virtualItem.index}
              key={virtualItem.key}
              ref={virtualizer.measureElement}
              role="listitem"
              style={{ transform: `translateY(${virtualItem.start.toString()}px)` }}
            >
              {renderRow(virtualItem.index)}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function fallbackRowStyle({
  count,
  index,
  paddingEnd,
  paddingStart,
}: Readonly<{
  count: number;
  index: number;
  paddingEnd: number;
  paddingStart: number;
}>): React.CSSProperties | undefined {
  if (count === 0) {
    return undefined;
  }
  return {
    paddingBottom: index === count - 1 ? paddingEnd : undefined,
    paddingTop: index === 0 ? paddingStart : undefined,
  };
}

function virtualRowClassName({
  count,
  index,
  rowSpacing,
  virtualized,
}: Readonly<{
  count: number;
  index: number;
  rowSpacing: "default" | "compact";
  virtualized: boolean;
}>): string {
  if (rowSpacing === "compact") {
    return cx("pb-[var(--space-2)]", index === count - 1 && "pb-0");
  }
  // Single-direction top gap so the inter-row spacing is exactly one spacing step (between-element level)
  // rather than the doubled top+bottom padding it would otherwise accumulate. Top/bottom insets are owned
  // by paddingStart/paddingEnd on the list container.
  return cx(virtualized ? index !== 0 && "pt-[var(--space-3)]" : "pt-[var(--space-3)] first:pt-0");
}

function fallbackRowKey<TItem>({
  emptyCount,
  getItemKey,
  headerCount,
  index,
  itemStartIndex,
  items,
  nextBoundaryIndex,
  previousBoundaryCount,
}: Readonly<{
  emptyCount: number;
  getItemKey: (item: TItem) => string;
  headerCount: number;
  index: number;
  itemStartIndex: number;
  items: readonly TItem[];
  nextBoundaryIndex: number | null;
  previousBoundaryCount: number;
}>): string {
  if (previousBoundaryCount > 0 && index === 0) {
    return "boundary-previous";
  }
  if (headerCount > 0 && index === previousBoundaryCount) {
    return "header";
  }
  if (emptyCount > 0 && index === itemStartIndex) {
    return "empty";
  }
  const item = items[index - itemStartIndex];
  if (item !== undefined) {
    return getItemKey(item);
  }
  return nextBoundaryIndex === index ? "boundary-next" : `placeholder-${index.toString()}`;
}

function renderVirtualRow<TItem>({
  empty,
  emptyCount,
  header,
  itemStartIndex,
  isFetchingNextPage,
  item,
  legacyPlaceholderIndex,
  loadingLabel,
  nextBoundary,
  nextBoundaryIndex,
  previousBoundary,
  renderItem,
  virtualIndex,
}: Readonly<{
  empty: ReactNode | undefined;
  emptyCount: number;
  header: ReactNode | undefined;
  itemStartIndex: number;
  isFetchingNextPage: boolean;
  item: TItem | undefined;
  legacyPlaceholderIndex: number | null;
  loadingLabel: string;
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  nextBoundaryIndex: number | null;
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  renderItem: (item: TItem) => ReactNode;
  virtualIndex: number;
}>): ReactNode {
  if (previousBoundary !== undefined && virtualIndex === 0) {
    return <InfiniteListBoundary direction="previous" state={previousBoundary} />;
  }
  if (header !== undefined && virtualIndex === (previousBoundary === undefined ? 0 : 1)) {
    return header;
  }
  if (emptyCount > 0 && virtualIndex === itemStartIndex) {
    return empty;
  }
  if (nextBoundary !== undefined && virtualIndex === nextBoundaryIndex) {
    return <InfiniteListBoundary direction="next" state={nextBoundary} />;
  }
  if (legacyPlaceholderIndex === virtualIndex) {
    return <VirtualizedPlaceholder loading={isFetchingNextPage} loadingLabel={loadingLabel} />;
  }
  if (item === undefined) {
    return null;
  }
  return renderItem(item);
}

function VirtualizedPlaceholder({
  loading,
  loadingLabel,
}: Readonly<{ loading: boolean; loadingLabel: string }>) {
  return (
    <div
      aria-label={loading ? loadingLabel : undefined}
      aria-live="polite"
      className="grid min-h-12 place-items-center"
      role={loading ? "status" : undefined}
    >
      {loading ? (
        <>
          <Spinner size="sm" />
          <span className="sr-only">{loadingLabel}</span>
        </>
      ) : null}
    </div>
  );
}

type VirtualizedLeadingAnchor = Readonly<{
  itemKey: string;
  inRowOffset: number;
}>;

function virtualizedListLayout({
  empty,
  hasHeader,
  hasNextPage,
  itemCount,
  nextBoundary,
  previousBoundary,
}: Readonly<{
  empty: ReactNode | undefined;
  hasHeader: boolean;
  hasNextPage: boolean;
  itemCount: number;
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
}>) {
  const previousBoundaryCount = previousBoundary === undefined ? 0 : 1;
  const headerCount = hasHeader ? 1 : 0;
  const emptyCount = itemCount === 0 && empty !== undefined ? 1 : 0;
  const itemStartIndex = previousBoundaryCount + headerCount;
  const contentCount = Math.max(itemCount, emptyCount);
  const nextBoundaryCount = nextBoundary === undefined ? 0 : 1;
  const placeholderCount = nextBoundary === undefined && hasNextPage ? 1 : 0;
  return {
    contentCount,
    count: itemStartIndex + contentCount + nextBoundaryCount + placeholderCount,
    emptyCount,
    headerCount,
    itemStartIndex,
    legacyPlaceholderIndex: nextBoundary === undefined && hasNextPage ? itemStartIndex + contentCount : null,
    nextBoundaryIndex: nextBoundary === undefined ? null : itemStartIndex + contentCount,
    previousBoundaryCount,
  };
}

function fallbackVirtualIndexes({
  count,
  estimateSize,
  pinnedIndexes,
}: Readonly<{
  count: number;
  estimateSize: () => number;
  pinnedIndexes: ReadonlySet<number>;
}>): number[] {
  if (count === 0) {
    return [];
  }
  const visibleCount = Math.max(1, Math.ceil(600 / Math.max(1, estimateSize())));
  const range: Range = {
    count,
    startIndex: 0,
    endIndex: Math.min(count - 1, visibleCount - 1),
    overscan: 6,
  };
  return pinnedVirtualRangeExtractor(range, pinnedIndexes);
}
