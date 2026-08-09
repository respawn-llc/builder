import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, type ReactNode } from "react";
import { type Range, type VirtualItem, useVirtualizer } from "@tanstack/react-virtual";

import { cx } from "./classes";
import { InfiniteListBoundary, type VirtualizedInfiniteListBoundaryState } from "./InfiniteListBoundary";
import { Spinner } from "./Spinner";
import { resolveVirtualizedInitialScroll } from "./virtualizedInfiniteListInitialScroll";
import { resolveLoadMore } from "./virtualizedInfiniteListLoadMore";
import { pinnedVirtualRangeExtractor } from "./virtualizedPinnedRange";
import { shouldAdjustScrollForVirtualizedResize } from "./virtualizedResizePolicy";
import { dataIndexForVirtualIndex, headerIndex, virtualIndexForDataIndex, virtualizedListLayout } from "./virtualizedInfiniteListLayout";
import {
  requireVirtualizedPixelOffsetRequest,
  type VirtualizedPixelOffsetRequest,
} from "./virtualizedPixelOffsetRequest";

export type { VirtualizedInfiniteListBoundaryState } from "./InfiniteListBoundary";

export type VirtualizedInfiniteListProps<TItem> = Readonly<{
  items: readonly TItem[];
  getItemKey: (item: TItem) => string;
  renderItem: (item: TItem, itemIndex: number) => ReactNode;
  header?: ReactNode | undefined;
  empty?: ReactNode | undefined;
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  loadingLabel: string;
  loadMoreKey?: string | undefined;
  onLoadMore: () => void;
  estimateSize: () => number;
  id?: string | undefined;
  ariaLabel?: string | undefined;
  role?: "list" | "listbox" | undefined;
  rowSpacing?: "default" | "compact" | "tight" | undefined;
  testId?: string | undefined;
  initialScrollKey?: string | undefined;
  initialScrollRequestKey?: string | undefined;
  initialScrollAlign?: "auto" | "start" | undefined;
  paddingEnd?: number | undefined;
  paddingStart?: number | undefined;
  className?: string | undefined;
  nonAdjustingResizeItemKey?: string | undefined;
  hasPreviousPage?: boolean | undefined;
  isFetchingPreviousPage?: boolean | undefined;
  previousLoadKey?: string | undefined;
  previousLoadItemKey?: string | undefined;
  onLoadPrevious?: (() => void) | undefined;
  previousBoundary?: VirtualizedInfiniteListBoundaryState | undefined;
  nextBoundary?: VirtualizedInfiniteListBoundaryState | undefined;
  onScrollElementChange?: ((element: HTMLDivElement | null) => void) | undefined;
  pinnedItemKeys?: ReadonlySet<string> | undefined;
  pixelOffsetRequest?: VirtualizedPixelOffsetRequest | undefined;
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
  id,
  ariaLabel,
  role = "list",
  rowSpacing = "default",
  testId,
  initialScrollKey,
  initialScrollRequestKey,
  initialScrollAlign = "start",
  paddingEnd = 0,
  paddingStart = 0,
  className,
  nonAdjustingResizeItemKey,
  hasPreviousPage = false,
  isFetchingPreviousPage = false,
  previousLoadKey,
  previousLoadItemKey,
  onLoadPrevious,
  previousBoundary,
  nextBoundary,
  onScrollElementChange,
  pinnedItemKeys,
  pixelOffsetRequest,
}: VirtualizedInfiniteListProps<TItem>) {
  const validatedPixelOffsetRequest = requireVirtualizedPixelOffsetRequest(pixelOffsetRequest);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const setScrollElement = useCallback(
    (element: HTMLDivElement | null) => {
      scrollRef.current = element;
      onScrollElementChange?.(element);
    },
    [onScrollElementChange],
  );
  const lastInitialScrollKeyRef = useRef<string | null>(null);
  const lastPixelOffsetKeyRef = useRef<string | null>(null);
  const lastLoadPreviousKeyRef = useRef<string | null>(null);
  const lastLoadMoreKeyRef = useRef<string | null>(null);
  const wasFetchingPreviousPageRef = useRef(false);
  const wasFetchingNextPageRef = useRef(false);
  const leadingAnchorRef = useRef<VirtualizedLeadingAnchor | null>(null);
  const previousItemKeysRef = useRef<readonly string[]>([]);
  const {
    count,
    emptyIndex,
    emptyCount,
    itemStartIndex,
    legacyPlaceholderIndex,
    nextBoundaryIndex,
    previousBoundaryIndex,
  } = virtualizedListLayout({
    empty,
    getItemKey,
    hasHeader: header !== undefined,
    hasNextPage,
    itemCount: items.length,
    items,
    nextBoundary,
    previousBoundary,
    previousLoadItemKey,
  });
  const pinnedIndexes = useMemo(() => {
    if (pinnedItemKeys === undefined || pinnedItemKeys.size === 0) {
      return new Set<number>();
    }
    const indexes = new Set<number>();
    items.forEach((item, index) => {
      if (pinnedItemKeys.has(getItemKey(item))) {
        indexes.add(virtualIndexForDataIndex(itemStartIndex, index, previousBoundaryIndex));
      }
    });
    return indexes;
  }, [getItemKey, itemStartIndex, items, pinnedItemKeys, previousBoundaryIndex]);
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
      if (previousBoundary !== undefined && index === previousBoundaryIndex) {
        return "boundary-previous";
      }
      if (header !== undefined && index === headerIndex(previousBoundaryIndex)) {
        return "header";
      }
      if (items.length === 0 && empty !== undefined && index === emptyIndex) {
        return "empty";
      }
      const dataIndex = dataIndexForVirtualIndex(index, itemStartIndex, previousBoundaryIndex);
      const item = dataIndex === null ? undefined : items[dataIndex];
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
      emptyIndex,
      headerIndex: headerIndex(previousBoundaryIndex),
      isFetchingNextPage,
      item: (() => {
        const dataIndex = dataIndexForVirtualIndex(virtualIndex, itemStartIndex, previousBoundaryIndex);
        return dataIndex === null ? undefined : items[dataIndex];
      })(),
      itemIndex: dataIndexForVirtualIndex(virtualIndex, itemStartIndex, previousBoundaryIndex),
      legacyPlaceholderIndex,
      loadingLabel,
      nextBoundary,
      nextBoundaryIndex,
      previousBoundary,
      previousBoundaryIndex,
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
          dataIndexForVirtualIndex(item.index, itemStartIndex, previousBoundaryIndex) !== null &&
          item.end > scrollTop,
      );
    if (virtualItem !== undefined) {
      const dataIndex = dataIndexForVirtualIndex(virtualItem.index, itemStartIndex, previousBoundaryIndex);
      const item = dataIndex === null ? undefined : items[dataIndex];
      leadingAnchorRef.current =
        item === undefined ? null : { itemKey: getItemKey(item), inRowOffset: scrollTop - virtualItem.start };
      return;
    }
    const estimatedSize = Math.max(1, estimateSize());
    const estimatedVirtualIndex = Math.max(
      itemStartIndex,
      Math.floor(Math.max(0, scrollTop - paddingStart) / estimatedSize),
    );
    const estimatedDataIndex = dataIndexForVirtualIndex(
      estimatedVirtualIndex,
      itemStartIndex,
      previousBoundaryIndex,
    );
    const dataIndex = Math.min(items.length - 1, estimatedDataIndex ?? 0);
    const item = items[dataIndex];
    leadingAnchorRef.current =
      item === undefined
        ? null
        : {
            itemKey: getItemKey(item),
            inRowOffset: scrollTop - (paddingStart + estimatedVirtualIndex * estimatedSize),
          };
  }, [estimateSize, getItemKey, itemStartIndex, items, paddingStart, previousBoundaryIndex, virtualizer]);

  useEffect(() => {
    if (initialScrollKey === undefined) {
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
    const scrollIndex =
      previousBoundaryIndex !== null && scroll.scrollIndex >= previousBoundaryIndex
        ? scroll.scrollIndex + 1
        : scroll.scrollIndex;
    virtualizer.scrollToIndex(scrollIndex, { align: initialScrollAlign, behavior: "auto" });
  }, [
    getItemKey,
    initialScrollAlign,
    initialScrollKey,
    initialScrollRequestKey,
    itemStartIndex,
    items,
    previousBoundaryIndex,
    virtualizer,
  ]);

  useLayoutEffect(() => {
    if (
      validatedPixelOffsetRequest === undefined ||
      items.length === 0 ||
      scrollRef.current === null ||
      lastPixelOffsetKeyRef.current === validatedPixelOffsetRequest.key
    ) {
      return;
    }
    scrollRef.current.scrollTop = validatedPixelOffsetRequest.offsetPx;
    virtualizer.scrollToOffset(scrollRef.current.scrollTop, { behavior: "auto" });
    lastPixelOffsetKeyRef.current = validatedPixelOffsetRequest.key;
  }, [items.length, validatedPixelOffsetRequest, virtualizer]);

  useLayoutEffect(() => {
    const currentKeys = items.map(getItemKey);
    const previousKeys = previousItemKeysRef.current;
    const anchor = leadingAnchorRef.current;
    const element = scrollRef.current;
    if (previousKeys.length > 0 && anchor !== null && element !== null) {
      const previousIndex = previousKeys.indexOf(anchor.itemKey);
      const currentIndex = currentKeys.indexOf(anchor.itemKey);
      if (previousIndex >= 0 && currentIndex >= 0) {
        const virtualIndex = virtualIndexForDataIndex(itemStartIndex, currentIndex, previousBoundaryIndex);
        const measuredOffset = isFallbackRendering
          ? undefined
          : virtualizer.getOffsetForIndex(virtualIndex, "start")?.[0];
        const rowOffset = measuredOffset ?? paddingStart + virtualIndex * Math.max(1, estimateSize());
        const scrollOffset = rowOffset + anchor.inRowOffset;
        if (element.scrollTop !== scrollOffset) element.scrollTop = scrollOffset;
        if (!isFallbackRendering) virtualizer.scrollToOffset(element.scrollTop, { behavior: "auto" });
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
    previousBoundaryIndex,
    virtualizer,
  ]);

  useEffect(() => {
    if (onLoadPrevious === undefined) {
      return;
    }
    const firstVisibleIndex = visibleIndexes[0];
    const atPreviousEdge =
      previousLoadItemKey === undefined
        ? firstVisibleIndex !== undefined && firstVisibleIndex <= itemStartIndex
        : visibleIndexes.some((virtualIndex) => {
            const dataIndex = dataIndexForVirtualIndex(virtualIndex, itemStartIndex, previousBoundaryIndex);
            const item = dataIndex === null ? undefined : items[dataIndex];
            return item !== undefined && getItemKey(item) === previousLoadItemKey;
          });
    const decision = resolveLoadMore({
      atBottom: atPreviousEdge,
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
    getItemKey,
    isFetchingPreviousPage,
    itemStartIndex,
    items,
    items.length,
    onLoadPrevious,
    previousBoundaryIndex,
    previousLoadItemKey,
    previousLoadKey,
    visibleIndexes,
  ]);

  useEffect(() => {
    const lastVisibleIndex = visibleIndexes.at(-1);
    const lastDataIndex =
      items.length === 0
        ? itemStartIndex
        : virtualIndexForDataIndex(itemStartIndex, items.length - 1, previousBoundaryIndex);
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
    previousBoundaryIndex,
    visibleIndexes,
  ]);

  if (count > 0 && virtualItems.length === 0) {
    return (
      <div
        aria-label={ariaLabel}
        className={className}
        data-testid={testId}
        id={id}
        onScroll={captureLeadingAnchor}
        ref={setScrollElement}
        role={role}
      >
        {fallbackIndexes.map((index) => (
          <div
            className={virtualRowClassName({ count, index, rowSpacing, virtualized: false })}
            key={fallbackRowKey({
              emptyCount,
              emptyIndex,
              getItemKey,
              headerIndex: header === undefined ? -1 : headerIndex(previousBoundaryIndex),
              index,
              itemStartIndex,
              items,
              nextBoundaryIndex,
              previousBoundaryIndex,
            })}
            role={virtualizedRowRole(role)}
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
      id={id}
      onScroll={captureLeadingAnchor}
      ref={setScrollElement}
      role={role}
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
              role={virtualizedRowRole(role)}
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

function virtualizedRowRole(role: "list" | "listbox"): "listitem" | "presentation" {
  return role === "listbox" ? "presentation" : "listitem";
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
  rowSpacing: "default" | "compact" | "tight";
  virtualized: boolean;
}>): string {
  if (rowSpacing === "compact") {
    return cx("pb-[var(--space-2)]", index === count - 1 && "pb-0");
  }
  if (rowSpacing === "tight") {
    return cx("pb-[var(--space-1)]", index === count - 1 && "pb-0");
  }
  // Single-direction top gap so the inter-row spacing is exactly one spacing step (between-element level)
  // rather than the doubled top+bottom padding it would otherwise accumulate. Top/bottom insets are owned
  // by paddingStart/paddingEnd on the list container.
  return cx(virtualized ? index !== 0 && "pt-[var(--space-3)]" : "pt-[var(--space-3)] first:pt-0");
}

function fallbackRowKey<TItem>({
  emptyCount,
  emptyIndex,
  getItemKey,
  headerIndex,
  index,
  itemStartIndex,
  items,
  nextBoundaryIndex,
  previousBoundaryIndex,
}: Readonly<{
  emptyCount: number;
  emptyIndex: number;
  getItemKey: (item: TItem) => string;
  headerIndex: number;
  index: number;
  itemStartIndex: number;
  items: readonly TItem[];
  nextBoundaryIndex: number | null;
  previousBoundaryIndex: number | null;
}>): string {
  if (previousBoundaryIndex !== null && index === previousBoundaryIndex) {
    return "boundary-previous";
  }
  if (headerIndex >= 0 && index === headerIndex) {
    return "header";
  }
  if (emptyCount > 0 && index === emptyIndex) {
    return "empty";
  }
  const dataIndex = dataIndexForVirtualIndex(index, itemStartIndex, previousBoundaryIndex);
  const item = dataIndex === null ? undefined : items[dataIndex];
  if (item !== undefined) {
    return getItemKey(item);
  }
  return nextBoundaryIndex === index ? "boundary-next" : `placeholder-${index.toString()}`;
}

function renderVirtualRow<TItem>({
  empty,
  emptyCount,
  emptyIndex,
  header,
  headerIndex,
  isFetchingNextPage,
  item,
  itemIndex,
  legacyPlaceholderIndex,
  loadingLabel,
  nextBoundary,
  nextBoundaryIndex,
  previousBoundary,
  previousBoundaryIndex,
  renderItem,
  virtualIndex,
}: Readonly<{
  empty: ReactNode | undefined;
  emptyCount: number;
  emptyIndex: number;
  header: ReactNode | undefined;
  headerIndex: number;
  isFetchingNextPage: boolean;
  item: TItem | undefined;
  itemIndex: number | null;
  legacyPlaceholderIndex: number | null;
  loadingLabel: string;
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  nextBoundaryIndex: number | null;
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  previousBoundaryIndex: number | null;
  renderItem: (item: TItem, itemIndex: number) => ReactNode;
  virtualIndex: number;
}>): ReactNode {
  if (previousBoundary !== undefined && virtualIndex === previousBoundaryIndex) {
    return <InfiniteListBoundary direction="previous" state={previousBoundary} />;
  }
  if (header !== undefined && virtualIndex === headerIndex) {
    return header;
  }
  if (emptyCount > 0 && virtualIndex === emptyIndex) {
    return empty;
  }
  if (nextBoundary !== undefined && virtualIndex === nextBoundaryIndex) {
    return <InfiniteListBoundary direction="next" state={nextBoundary} />;
  }
  if (legacyPlaceholderIndex === virtualIndex) {
    return <VirtualizedPlaceholder loading={isFetchingNextPage} loadingLabel={loadingLabel} />;
  }
  if (item === undefined || itemIndex === null) {
    return null;
  }
  return renderItem(item, itemIndex);
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
