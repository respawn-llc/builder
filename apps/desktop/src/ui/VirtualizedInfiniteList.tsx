import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  type AriaRole,
  type HTMLAttributes,
  type ReactNode,
} from "react";
import { type Range, type VirtualItem, useVirtualizer } from "@tanstack/react-virtual";

import { cx } from "./classes";
import type { VirtualizedInfiniteListBoundaryState } from "./InfiniteListBoundary";
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
import {
  useVirtualizedItemVisibilityTriggers,
  type VirtualizedItemVisibilityTrigger,
} from "./virtualizedItemVisibilityTriggers";
import {
  recoverableLeadingAnchor,
  resolveAnchorEntryIndex,
  type VirtualizedAnchorEntry,
  type VirtualizedLeadingAnchor,
} from "./virtualizedLeadingAnchor";
import {
  fallbackVirtualizedRowStyle,
  renderVirtualizedRow,
  virtualizedRowClassName,
  virtualizedRowKey,
} from "./virtualizedInfiniteListRows";

export type { VirtualizedInfiniteListBoundaryState } from "./InfiniteListBoundary";
export type { VirtualizedItemVisibilityTrigger } from "./virtualizedItemVisibilityTriggers";

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
  role?: AriaRole | undefined;
  itemRole?: AriaRole | undefined;
  getItemWrapperProps?: ((item: TItem, itemIndex: number) => HTMLAttributes<HTMLDivElement>) | undefined;
  rowSpacing?: "default" | "compact" | "tight" | undefined;
  testId?: string | undefined;
  initialScrollKey?: string | undefined;
  initialScrollRequestKey?: string | undefined;
  initialScrollAlign?: "auto" | "start" | undefined;
  layoutChangeScrollBehavior?: "preserve-leading-item" | "natural" | undefined;
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
  stickyItemKeys?: ReadonlySet<string> | undefined;
  visibilityTriggers?: readonly VirtualizedItemVisibilityTrigger[] | undefined;
  pixelOffsetRequest?: VirtualizedPixelOffsetRequest | undefined;
  orientation?: "vertical" | "horizontal" | undefined;
}>;

type VirtualizedInfiniteListResolvedProps<TItem> = Omit<
  VirtualizedInfiniteListProps<TItem>,
  | "role"
  | "rowSpacing"
  | "initialScrollAlign"
  | "layoutChangeScrollBehavior"
  | "paddingEnd"
  | "paddingStart"
  | "hasPreviousPage"
  | "isFetchingPreviousPage"
  | "orientation"
> & {
  role: AriaRole;
  itemRole: AriaRole;
  rowSpacing: "default" | "compact" | "tight";
  initialScrollAlign: "auto" | "start";
  layoutChangeScrollBehavior: "preserve-leading-item" | "natural";
  paddingEnd: number;
  paddingStart: number;
  hasPreviousPage: boolean;
  isFetchingPreviousPage: boolean;
  orientation: "vertical" | "horizontal";
};

function resolveVirtualizedInfiniteListProps<TItem>(
  props: VirtualizedInfiniteListProps<TItem>,
): VirtualizedInfiniteListResolvedProps<TItem> {
  return {
    ...props,
    role: props.role ?? "list",
    itemRole: props.itemRole ?? (props.role === "listbox" ? "presentation" : "listitem"),
    rowSpacing: props.rowSpacing ?? "default",
    initialScrollAlign: props.initialScrollAlign ?? "start",
    layoutChangeScrollBehavior: props.layoutChangeScrollBehavior ?? "preserve-leading-item",
    paddingEnd: props.paddingEnd ?? 0,
    paddingStart: props.paddingStart ?? 0,
    hasPreviousPage: props.hasPreviousPage ?? false,
    isFetchingPreviousPage: props.isFetchingPreviousPage ?? false,
    orientation: props.orientation ?? "vertical",
  };
}

export function VirtualizedInfiniteList<TItem>(props: VirtualizedInfiniteListProps<TItem>) {
  return <VirtualizedInfiniteListContent {...resolveVirtualizedInfiniteListProps(props)} />;
}

// prettier-ignore
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
  itemRole,
  getItemWrapperProps,
  rowSpacing,
  testId,
  initialScrollKey,
  initialScrollRequestKey,
  initialScrollAlign,
  layoutChangeScrollBehavior,
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
  stickyItemKeys,
  visibilityTriggers,
  pixelOffsetRequest,
  orientation,
}: VirtualizedInfiniteListResolvedProps<TItem>) {
  const horizontal = orientation === "horizontal";
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
  const previousBoundaryCount = Number(previousBoundary !== undefined) * Number(Number(!horizontal) + Number(items.length > 0) > 0);
  const headerCount = Number(header !== undefined);
  const emptyCount = Number(items.length === 0) * Number(empty !== undefined);
  const itemStartIndex = previousBoundaryCount + headerCount;
  const contentCount = Math.max(items.length, emptyCount);
  const nextBoundaryIndex = itemStartIndex + contentCount;
  const hasLegacyPlaceholder = Number(nextBoundary === undefined) * Number(hasNextPage); const legacyPlaceholderIndex = [null, nextBoundaryIndex][hasLegacyPlaceholder] ?? null;
  const count = nextBoundaryIndex + Number(nextBoundary !== undefined) + hasLegacyPlaceholder;
  const retainedItemKeys = useMemo(
    () => new Set([...(pinnedItemKeys ?? []), ...(stickyItemKeys ?? [])]),
    [pinnedItemKeys, stickyItemKeys],
  );
  const pinnedIndexes = useMemo(() => {
    const indexes = new Set<number>();
    if (Number(horizontal) * Number(header !== undefined) === 1) indexes.add(0);
    if (retainedItemKeys.size === 0) {
      return indexes;
    }
    items.forEach((item, index) => {
      if (retainedItemKeys.has(getItemKey(item))) {
        indexes.add(itemStartIndex + index);
      }
    });
    return indexes;
  }, [getItemKey, header, horizontal, itemStartIndex, items, retainedItemKeys]);
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
    getItemKey: (index) => virtualizedRowKey({ emptyCount, horizontal, getItemKey, headerCount, index, itemStartIndex, items, nextBoundaryIndex, previousBoundaryCount }),
    ...[{}, { overscan: 6 }][Number(!horizontal)],
    horizontal,
    rangeExtractor: (range) => pinnedVirtualRangeExtractor(range, pinnedIndexes),
  });
  virtualizer.shouldAdjustScrollPositionOnItemSizeChange =
    nonAdjustingResizeItemKey === undefined
      ? undefined
      : (item: VirtualItem) =>
          shouldAdjustScrollForVirtualizedResize(nonAdjustingResizeItemKey, String(item.key));
  const virtualItems = virtualizer.getVirtualItems();
  const isFallbackRendering = virtualItems.length === 0;
  const fallbackIndexes = fallbackVirtualIndexes({ count, estimateSize, pinnedIndexes });
  const visibleIndexes = virtualizedVisibleIndexes(fallbackIndexes, virtualItems);

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
    if (validatedPixelOffsetRequest === undefined || items.length === 0 || scrollRef.current === null || lastPixelOffsetKeyRef.current === validatedPixelOffsetRequest.key) return;
    const scrollOffset = validatedPixelOffsetRequest.offsetPx;
    scrollRef.current[horizontal ? "scrollLeft" : "scrollTop"] = scrollOffset;
    virtualizer.scrollToOffset(scrollOffset, { behavior: "auto" });
    lastPixelOffsetKeyRef.current = validatedPixelOffsetRequest.key;
    pixelOffsetAppliedKeyRef.current = validatedPixelOffsetRequest.key;
  }, [horizontal, items.length, validatedPixelOffsetRequest, virtualizer]);

  const onScroll = useVirtualizedLeadingAnchor({
    behavior: layoutChangeScrollBehavior,
    estimateSize,
    getItemAnchorKey: getItemAnchorKeyForItem,
    getItemOccurrenceKey: getItemOccurrenceKeyForItem,
    isFallbackRendering,
    itemStartIndex,
    items,
    paddingStart,
    pixelOffsetAppliedKeyRef,
    pixelOffsetRequestKey: validatedPixelOffsetRequest?.key,
    orientation,
    scrollRef,
    virtualItems,
    virtualizer,
  });

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
    orientation,
    wasFetchingNextPageRef: wasFetchingPreviousPageRef,
  });

  useVirtualizedItemVisibilityTriggers({
    getItemKey,
    itemStartIndex,
    items,
    triggers: visibilityTriggers,
    visibleIndexes,
  });

  useVirtualizedLoadMore({
    atEdge: resolveNextLoadEdge(itemStartIndex, items.length, visibleIndexes),
    hasNextPage,
    isFetchingNextPage,
    lastLoadMoreKeyRef,
    loadMoreKey: loadMoreKey ?? items.length.toString(),
    onLoadMore,
    orientation,
    wasFetchingNextPageRef,
  });

  const renderRow = (virtualIndex: number): ReactNode => renderVirtualizedRow({ empty, emptyCount, header, headerCount, horizontal, item: items[virtualIndex - itemStartIndex], isFetchingNextPage, itemStartIndex, legacyPlaceholderIndex, loadingLabel, nextBoundary, nextBoundaryIndex, previousBoundary, previousBoundaryCount, renderItem, virtualIndex });
  const getRowKey = (index: number): string => virtualizedRowKey({ emptyCount, getItemKey, headerCount, horizontal, index, itemStartIndex, items, nextBoundaryIndex, previousBoundaryCount });
  const isStickyRow = (index: number, itemKey: string | undefined): boolean => (horizontal && headerCount > 0 && index === 0) || (itemKey !== undefined && stickyItemKeys?.has(itemKey) === true);
  const hasHorizontalBoundary = Number(horizontal) * Number(Number(emptyCount > 0) + Number(previousBoundary !== undefined) + Number(nextBoundary !== undefined) > 0) > 0;
  if (Number(count > 0) * Number(virtualItems.length === 0) === 1) {
    return (
      <div
        aria-label={ariaLabel}
        className={cx(className, ["", "flex"][Number(horizontal)])}
        data-testid={testId}
        id={id}
        onScroll={onScroll}
        ref={setScrollElement}
        role={role}
      >
        {fallbackIndexes.map((index) => {
          const itemIndex = index - itemStartIndex;
          const item = items[itemIndex];
          const itemKey = item === undefined ? undefined : getItemKey(item);
          const wrapperProps = item === undefined ? undefined : getItemWrapperProps?.(item, itemIndex);
          const sticky = isStickyRow(index, itemKey);
          return (
            <div
              {...wrapperProps}
              className={cx((["", "sticky top-0 z-[1]", "absolute top-0 left-0 w-full", "sticky top-0 z-[1] w-full", "", "sticky z-[1]", "absolute top-0 left-0 w-max", "sticky z-[1] w-max"][Number(horizontal) * 4 + Number(sticky)] ?? ""), virtualizedRowClassName({ count, index, orientation, rowSpacing, virtualized: false }), wrapperProps?.className)}
              key={getRowKey(index)}
              role={itemRole}
              style={{ ...fallbackVirtualizedRowStyle({ count, index, orientation, paddingEnd, paddingStart }), left: [undefined, paddingStart][Number(horizontal) * Number(sticky)] }}
            >
              {renderRow(index)}
            </div>
          );
        })}
      </div>
    );
  }
  return (
    <div
      aria-label={ariaLabel}
      className={cx(className, ["", "flex"][Number(horizontal)])}
      data-testid={testId}
      id={id}
      onScroll={onScroll}
      ref={setScrollElement}
      role={role}
    >
      <div
        className={
          horizontal
            ? cx("relative w-max shrink-0", hasHorizontalBoundary ? "min-h-12" : "min-h-7")
            : "relative w-full"
        }
        style={
          horizontal
            ? { width: `${virtualizer.getTotalSize().toString()}px` }
            : { height: `${virtualizer.getTotalSize().toString()}px` }
        }
      >
        {virtualItems.map((virtualItem) => {
          const itemIndex = virtualItem.index - itemStartIndex;
          const item = items[itemIndex];
          const itemKey = item === undefined ? undefined : getItemKey(item);
          const wrapperProps = item === undefined ? undefined : getItemWrapperProps?.(item, itemIndex);
          const sticky = isStickyRow(virtualItem.index, itemKey);
          return (
            <div
              {...wrapperProps}
              className={cx((["", "sticky top-0 z-[1]", "absolute top-0 left-0 w-full", "sticky top-0 z-[1] w-full", "", "sticky z-[1]", "absolute top-0 left-0 w-max", "sticky z-[1] w-max"][Number(horizontal) * 4 + 2 + Number(sticky)] ?? ""), virtualizedRowClassName({ count, index: virtualItem.index, orientation, rowSpacing, virtualized: true }), wrapperProps?.className)}
              data-index={virtualItem.index}
              key={virtualItem.key}
              ref={virtualizer.measureElement}
              role={itemRole}
              style={[{ ...wrapperProps?.style, transform: `${horizontal ? "translateX" : "translateY"}(${virtualItem.start.toString()}px)` }, { ...wrapperProps?.style, left: paddingStart }, wrapperProps?.style][Number(sticky) * (Number(!horizontal) + 1)]}
            >
              {renderRow(virtualItem.index)}
            </div>
          );
        })}
      </div>
    </div>
  );
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

function virtualizedVisibleIndexes(
  fallbackIndexes: readonly number[],
  virtualItems: readonly VirtualItem[],
): readonly number[] {
  return virtualItems.length === 0 ? fallbackIndexes : virtualItems.map((virtualItem) => virtualItem.index);
}

function useVirtualizedLeadingAnchor<TItem>({
  behavior,
  estimateSize,
  getItemAnchorKey,
  getItemOccurrenceKey,
  isFallbackRendering,
  itemStartIndex,
  items,
  orientation,
  paddingStart,
  pixelOffsetAppliedKeyRef,
  pixelOffsetRequestKey,
  scrollRef,
  virtualItems,
  virtualizer,
}: Readonly<{
  behavior: "preserve-leading-item" | "natural";
  estimateSize: () => number;
  getItemAnchorKey: (item: TItem) => string;
  getItemOccurrenceKey: (item: TItem) => string;
  isFallbackRendering: boolean;
  itemStartIndex: number;
  items: readonly TItem[];
  orientation: "vertical" | "horizontal";
  paddingStart: number;
  pixelOffsetAppliedKeyRef: { current: string | null };
  pixelOffsetRequestKey: string | undefined;
  scrollRef: { current: HTMLDivElement | null };
  virtualItems: readonly VirtualItem[];
  virtualizer: ReturnType<typeof useVirtualizer<HTMLDivElement, Element>>;
}>): (() => void) | undefined {
  const leadingAnchorRef = useRef<VirtualizedLeadingAnchor | null>(null);
  const previousAnchorEntriesRef = useRef<readonly VirtualizedAnchorEntry[]>([]);
  const captureLeadingAnchor = useCallback(() => {
    const element = scrollRef.current;
    if (element === null || items.length === 0) {
      leadingAnchorRef.current = null;
      return;
    }
    const scrollOffset = orientation === "horizontal" ? element.scrollLeft : element.scrollTop;
    const virtualItem = virtualizer.getVirtualItems().find((item) => {
      if (item.index < itemStartIndex || item.index >= itemStartIndex + items.length) {
        return false;
      }
      if (orientation === "horizontal") {
        return item.start < scrollOffset + element.clientWidth && item.end > scrollOffset;
      }
      return item.start <= scrollOffset && item.end > scrollOffset;
    });
    if (virtualItem !== undefined) {
      const item = items[virtualItem.index - itemStartIndex];
      leadingAnchorRef.current =
        item === undefined
          ? null
          : {
              anchorKey: getItemAnchorKey(item),
              occurrenceKey: getItemOccurrenceKey(item),
              inRowOffset:
                orientation === "horizontal"
                  ? virtualItem.start - scrollOffset
                  : scrollOffset - virtualItem.start,
            };
      return;
    }
    if (orientation === "horizontal") {
      leadingAnchorRef.current = null;
      return;
    }
    const estimatedSize = Math.max(1, estimateSize());
    const estimatedVirtualIndex = Math.max(
      itemStartIndex,
      Math.floor(Math.max(0, scrollOffset - paddingStart) / estimatedSize),
    );
    const dataIndex = Math.min(items.length - 1, estimatedVirtualIndex - itemStartIndex);
    const item = items[dataIndex];
    leadingAnchorRef.current =
      item === undefined
        ? null
        : {
            anchorKey: getItemAnchorKey(item),
            occurrenceKey: getItemOccurrenceKey(item),
            inRowOffset: scrollOffset - (paddingStart + estimatedVirtualIndex * estimatedSize),
          };
  }, [
    estimateSize,
    getItemAnchorKey,
    getItemOccurrenceKey,
    itemStartIndex,
    items,
    orientation,
    paddingStart,
    scrollRef,
    virtualizer,
  ]);

  useLayoutEffect(() => {
    if (behavior === "natural") {
      leadingAnchorRef.current = null;
      previousAnchorEntriesRef.current = [];
      return;
    }
    const currentEntries = items.map((item) => ({
      anchorKey: getItemAnchorKey(item),
      occurrenceKey: getItemOccurrenceKey(item),
    }));
    const previousEntries = previousAnchorEntriesRef.current;
    const anchor = recoverableLeadingAnchor(
      leadingAnchorRef.current,
      pixelOffsetAppliedKeyRef.current,
      pixelOffsetRequestKey,
    );
    if (previousEntries.length > 0 && anchor !== null && scrollRef.current !== null) {
      restoreLeadingAnchor({
        anchor,
        currentEntries,
        element: scrollRef.current,
        estimateSize,
        isFallbackRendering,
        itemStartIndex,
        orientation,
        paddingStart,
        previousEntries,
        virtualizer,
      });
    }
    previousAnchorEntriesRef.current = currentEntries;
    pixelOffsetAppliedKeyRef.current = null;
    captureLeadingAnchor();
  }, [
    behavior,
    captureLeadingAnchor,
    estimateSize,
    getItemAnchorKey,
    getItemOccurrenceKey,
    isFallbackRendering,
    itemStartIndex,
    items,
    orientation,
    paddingStart,
    pixelOffsetAppliedKeyRef,
    pixelOffsetRequestKey,
    scrollRef,
    virtualItems,
    virtualizer,
  ]);

  return behavior === "preserve-leading-item" ? captureLeadingAnchor : undefined;
}

function restoreLeadingAnchor({
  anchor,
  currentEntries,
  element,
  estimateSize,
  isFallbackRendering,
  itemStartIndex,
  orientation,
  paddingStart,
  previousEntries,
  virtualizer,
}: Readonly<{
  anchor: VirtualizedLeadingAnchor;
  currentEntries: readonly VirtualizedAnchorEntry[];
  element: HTMLDivElement;
  estimateSize: () => number;
  isFallbackRendering: boolean;
  itemStartIndex: number;
  orientation: "vertical" | "horizontal";
  paddingStart: number;
  previousEntries: readonly VirtualizedAnchorEntry[];
  virtualizer: ReturnType<typeof useVirtualizer<HTMLDivElement, Element>>;
}>): void {
  const previousIndex = resolveAnchorEntryIndex(previousEntries, anchor);
  const currentIndex = resolveAnchorEntryIndex(currentEntries, anchor);
  if (previousIndex < 0 || currentIndex < 0) {
    return;
  }
  const virtualIndex = itemStartIndex + currentIndex;
  if (orientation === "horizontal") {
    const measuredItem = virtualizer.getVirtualItems().find((item) => item.index === virtualIndex);
    if (measuredItem === undefined) {
      return;
    }
    const scrollOffset = measuredItem.start - anchor.inRowOffset;
    if (element.scrollLeft === scrollOffset) {
      return;
    }
    element.scrollLeft = scrollOffset;
    virtualizer.scrollToOffset(scrollOffset, { behavior: "auto" });
    return;
  }
  const measuredOffset = isFallbackRendering
    ? undefined
    : virtualizer.getOffsetForIndex(virtualIndex, "start")?.[0];
  const rowOffset = measuredOffset ?? paddingStart + virtualIndex * Math.max(1, estimateSize());
  const scrollOffset = rowOffset + anchor.inRowOffset;
  if (element.scrollTop === scrollOffset) {
    return;
  }
  element.scrollTop = scrollOffset;
  if (!isFallbackRendering) {
    virtualizer.scrollToOffset(scrollOffset, { behavior: "auto" });
  }
}
