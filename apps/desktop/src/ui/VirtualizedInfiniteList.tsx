import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, type ReactNode } from "react";
import { type Range, type VirtualItem, useVirtualizer } from "@tanstack/react-virtual";

import { cx } from "./classes";
import { InfiniteListBoundary, type VirtualizedInfiniteListBoundaryState } from "./InfiniteListBoundary";
import { Spinner } from "./Spinner";
import { resolveVirtualizedInitialScroll } from "./virtualizedInfiniteListInitialScroll";
import {
  resolveNextLoadEdge,
  resolvePreviousLoadEdge,
  useVirtualizedLoadMore,
} from "./virtualizedInfiniteListLoadMore";
import { pinnedVirtualRangeExtractor } from "./virtualizedPinnedRange";
import { shouldAdjustScrollForVirtualizedResize } from "./virtualizedResizePolicy";
import {
  requireVirtualizedPixelOffsetRequest,
  type VirtualizedPixelOffsetRequest,
} from "./virtualizedPixelOffsetRequest";

export type { VirtualizedInfiniteListBoundaryState } from "./InfiniteListBoundary";

export type VirtualizedInfiniteListProps<TItem> = Readonly<{
  items: readonly TItem[];
  getItemKey: (item: TItem) => string;
  getItemAnchorKey?: ((item: TItem) => string) | undefined;
  getItemOccurrenceKey?: ((item: TItem) => string) | undefined;
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

type VirtualizedInfiniteListResolvedProps<TItem> = Omit<
  VirtualizedInfiniteListProps<TItem>,
  | "role"
  | "rowSpacing"
  | "initialScrollAlign"
  | "paddingEnd"
  | "paddingStart"
  | "hasPreviousPage"
  | "isFetchingPreviousPage"
> & {
  role: "list" | "listbox";
  rowSpacing: "default" | "compact" | "tight";
  initialScrollAlign: "auto" | "start";
  paddingEnd: number;
  paddingStart: number;
  hasPreviousPage: boolean;
  isFetchingPreviousPage: boolean;
};

function resolveVirtualizedInfiniteListProps<TItem>(
  props: VirtualizedInfiniteListProps<TItem>,
): VirtualizedInfiniteListResolvedProps<TItem> {
  return {
    ...props,
    role: props.role ?? "list",
    rowSpacing: props.rowSpacing ?? "default",
    initialScrollAlign: props.initialScrollAlign ?? "start",
    paddingEnd: props.paddingEnd ?? 0,
    paddingStart: props.paddingStart ?? 0,
    hasPreviousPage: props.hasPreviousPage ?? false,
    isFetchingPreviousPage: props.isFetchingPreviousPage ?? false,
  };
}

export function VirtualizedInfiniteList<TItem>(props: VirtualizedInfiniteListProps<TItem>) {
  return <VirtualizedInfiniteListContent {...resolveVirtualizedInfiniteListProps(props)} />;
}

function VirtualizedInfiniteListContent<TItem>({
  items,
  getItemKey,
  getItemAnchorKey,
  getItemOccurrenceKey,
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
  role,
  rowSpacing,
  testId,
  initialScrollKey,
  initialScrollRequestKey,
  initialScrollAlign,
  paddingEnd,
  paddingStart,
  className,
  nonAdjustingResizeItemKey,
  hasPreviousPage,
  isFetchingPreviousPage,
  previousLoadKey,
  previousLoadItemKey,
  onLoadPrevious,
  previousBoundary,
  nextBoundary,
  onScrollElementChange,
  pinnedItemKeys,
  pixelOffsetRequest,
}: VirtualizedInfiniteListResolvedProps<TItem>) {
  const getItemAnchorKeyForItem = getItemAnchorKey ?? getItemKey;
  const getItemOccurrenceKeyForItem = getItemOccurrenceKey ?? getItemKey;
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
  const pixelOffsetAppliedKeyRef = useRef<string | null>(null);
  const lastLoadPreviousKeyRef = useRef<string | null>(null);
  const lastLoadMoreKeyRef = useRef<string | null>(null);
  const wasFetchingPreviousPageRef = useRef(false);
  const wasFetchingNextPageRef = useRef(false);
  const leadingAnchorRef = useRef<VirtualizedLeadingAnchor | null>(null);
  const previousAnchorEntriesRef = useRef<readonly VirtualizedAnchorEntry[]>([]);
  const previousBoundaryCount = Number(previousBoundary !== undefined);
  const headerCount = Number(header !== undefined);
  const emptyCount = Number(items.length === 0 && empty !== undefined);
  const itemStartIndex = previousBoundaryCount + headerCount;
  const contentCount = Math.max(items.length, emptyCount);
  const nextBoundaryIndex = itemStartIndex + contentCount;
  const legacyPlaceholderIndex = nextBoundary === undefined && hasNextPage ? nextBoundaryIndex : null;
  const count =
    nextBoundaryIndex + Number(nextBoundary !== undefined) + Number(legacyPlaceholderIndex !== null);
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
    // Pixel restoration and leading-anchor recovery issue absolute scroll
    // commands from layout effects. Do not re-enter React synchronously from
    // those lifecycle paths.
    useFlushSync: false,
    getItemKey: (index) =>
      virtualRowKey({
        emptyCount,
        getItemKey,
        headerCount,
        index,
        itemStartIndex,
        items,
        nextBoundaryIndex,
        previousBoundaryCount,
      }),
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
      headerIndex: previousBoundaryCount,
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
          item.index >= itemStartIndex &&
          item.index < itemStartIndex + items.length &&
          item.start <= scrollTop &&
          item.end > scrollTop,
      );
    if (virtualItem !== undefined) {
      const item = items[virtualItem.index - itemStartIndex];
      leadingAnchorRef.current =
        item === undefined
          ? null
          : {
              anchorKey: getItemAnchorKeyForItem(item),
              occurrenceKey: getItemOccurrenceKeyForItem(item),
              inRowOffset: scrollTop - virtualItem.start,
            };
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
            anchorKey: getItemAnchorKeyForItem(item),
            occurrenceKey: getItemOccurrenceKeyForItem(item),
            inRowOffset: scrollTop - (paddingStart + estimatedVirtualIndex * estimatedSize),
          };
  }, [
    estimateSize,
    getItemAnchorKeyForItem,
    getItemOccurrenceKeyForItem,
    itemStartIndex,
    items,
    paddingStart,
    virtualizer,
  ]);

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
    virtualizer.scrollToIndex(scroll.scrollIndex, { align: initialScrollAlign, behavior: "auto" });
  }, [
    getItemKey,
    initialScrollAlign,
    initialScrollKey,
    initialScrollRequestKey,
    itemStartIndex,
    items,
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
    pixelOffsetAppliedKeyRef.current = validatedPixelOffsetRequest.key;
  }, [items.length, validatedPixelOffsetRequest, virtualizer]);

  useLayoutEffect(() => {
    const currentEntries = items.map((item) => ({
      anchorKey: getItemAnchorKeyForItem(item),
      occurrenceKey: getItemOccurrenceKeyForItem(item),
    }));
    const previousEntries = previousAnchorEntriesRef.current;
    const anchor = recoverableLeadingAnchor(
      leadingAnchorRef.current,
      pixelOffsetAppliedKeyRef.current,
      validatedPixelOffsetRequest,
    );
    const element = scrollRef.current;
    if (previousEntries.length > 0 && anchor !== null && element !== null) {
      const resolvedPreviousIndex = resolveAnchorEntryIndex(previousEntries, anchor);
      const resolvedCurrentIndex = resolveAnchorEntryIndex(currentEntries, anchor);
      if (resolvedPreviousIndex >= 0 && resolvedCurrentIndex >= 0) {
        const virtualIndex = itemStartIndex + resolvedCurrentIndex;
        const measuredOffset = isFallbackRendering
          ? undefined
          : virtualizer.getOffsetForIndex(virtualIndex, "start")?.[0];
        const rowOffset = measuredOffset ?? paddingStart + virtualIndex * Math.max(1, estimateSize());
        const scrollOffset = rowOffset + anchor.inRowOffset;
        const scrollOffsetChanged = element.scrollTop !== scrollOffset;
        if (scrollOffsetChanged) element.scrollTop = scrollOffset;
        if (scrollOffsetChanged && !isFallbackRendering) {
          virtualizer.scrollToOffset(scrollOffset, { behavior: "auto" });
        }
      }
    }
    previousAnchorEntriesRef.current = currentEntries;
    pixelOffsetAppliedKeyRef.current = null;
    captureLeadingAnchor();
  }, [
    captureLeadingAnchor,
    estimateSize,
    getItemAnchorKeyForItem,
    getItemOccurrenceKeyForItem,
    isFallbackRendering,
    itemStartIndex,
    items,
    paddingStart,
    validatedPixelOffsetRequest,
    virtualItems,
    virtualizer,
  ]);

  useVirtualizedLoadMore({
    atEdge: resolvePreviousLoadEdge({
      getItemKey,
      itemStartIndex,
      items,
      previousLoadItemKey,
      visibleIndexes,
    }),
    hasNextPage: hasPreviousPage,
    isFetchingNextPage: isFetchingPreviousPage,
    lastLoadMoreKeyRef: lastLoadPreviousKeyRef,
    loadMoreKey: previousLoadKey ?? items.length.toString(),
    onLoadMore: onLoadPrevious,
    wasFetchingNextPageRef: wasFetchingPreviousPageRef,
  });

  useVirtualizedLoadMore({
    atEdge: resolveNextLoadEdge(itemStartIndex, items.length, visibleIndexes),
    hasNextPage,
    isFetchingNextPage,
    lastLoadMoreKeyRef,
    loadMoreKey: loadMoreKey ?? items.length.toString(),
    onLoadMore,
    wasFetchingNextPageRef,
  });

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
            key={virtualRowKey({
              emptyCount,
              getItemKey,
              headerCount,
              index,
              itemStartIndex,
              items,
              nextBoundaryIndex,
              previousBoundaryCount,
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

function virtualRowKey<TItem>({
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
  nextBoundaryIndex: number;
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
  headerIndex,
  isFetchingNextPage,
  item,
  itemStartIndex,
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
  headerIndex: number;
  isFetchingNextPage: boolean;
  item: TItem | undefined;
  itemStartIndex: number;
  legacyPlaceholderIndex: number | null;
  loadingLabel: string;
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  nextBoundaryIndex: number | null;
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  renderItem: (item: TItem, itemIndex: number) => ReactNode;
  virtualIndex: number;
}>): ReactNode {
  if (previousBoundary !== undefined && virtualIndex === 0) {
    return <InfiniteListBoundary direction="previous" state={previousBoundary} />;
  }
  if (header !== undefined && virtualIndex === headerIndex) {
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
  return renderItem(item, virtualIndex - itemStartIndex);
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
  anchorKey: string;
  occurrenceKey: string;
  inRowOffset: number;
}>;

function recoverableLeadingAnchor(
  anchor: VirtualizedLeadingAnchor | null,
  appliedPixelOffsetKey: string | null,
  request: VirtualizedPixelOffsetRequest | undefined,
): VirtualizedLeadingAnchor | null {
  return appliedPixelOffsetKey === request?.key ? null : anchor;
}

type VirtualizedAnchorEntry = Readonly<{
  anchorKey: string;
  occurrenceKey: string;
}>;

function findAnchorEntryIndex(
  entries: readonly VirtualizedAnchorEntry[],
  anchor: VirtualizedLeadingAnchor,
  includeOccurrence: boolean,
): number {
  return entries.findIndex(
    (entry) =>
      entry.anchorKey === anchor.anchorKey &&
      (!includeOccurrence || entry.occurrenceKey === anchor.occurrenceKey),
  );
}

function resolveAnchorEntryIndex(
  entries: readonly VirtualizedAnchorEntry[],
  anchor: VirtualizedLeadingAnchor,
): number {
  const occurrenceIndex = findAnchorEntryIndex(entries, anchor, true);
  return occurrenceIndex >= 0 ? occurrenceIndex : findAnchorEntryIndex(entries, anchor, false);
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
