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
import { type VirtualItem, useVirtualizer } from "@tanstack/react-virtual";

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
  renderVirtualizedInfiniteListRow,
  renderVirtualizedInfiniteListRows,
  fallbackVirtualIndexes,
  resolveVirtualizedInfiniteListLayout,
  resolveHorizontalBoundary,
  resolveVirtualizedContainerClassName,
  resolveVirtualizedInnerClassName,
  resolveVirtualizedInnerStyle,
  renderVirtualizedRow,
  type VirtualizedInfiniteListLayout,
  virtualizedScrollOffsetProperty,
  virtualizedVisibleIndexes,
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
  const layout: VirtualizedInfiniteListLayout = resolveVirtualizedInfiniteListLayout({
    empty,
    hasNextPage,
    header,
    horizontal,
    itemsLength: items.length,
    nextBoundary,
    previousBoundary,
  });
  const { count, itemStartIndex } = layout;
  const retainedItemKeys = useMemo(
    () => new Set([...(pinnedItemKeys ?? []), ...(stickyItemKeys ?? [])]),
    [pinnedItemKeys, stickyItemKeys],
  );
  const pinnedIndexes = useMemo(() => {
    const indexes = new Set<number>();
    if (horizontal && layout.headerIndex !== null) {
      indexes.add(layout.headerIndex);
    }
    if (retainedItemKeys.size === 0) {
      return indexes;
    }
    items.forEach((item, index) => {
      if (retainedItemKeys.has(getItemKey(item))) {
        indexes.add(itemStartIndex + index);
      }
    });
    return indexes;
  }, [getItemKey, horizontal, itemStartIndex, items, layout.headerIndex, retainedItemKeys]);
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
      virtualizedRowKey({
        getItemKey,
        index,
        items,
        layout,
      }),
    ...(horizontal ? {} : { overscan: 6 }),
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
    if (
      validatedPixelOffsetRequest === undefined ||
      items.length === 0 ||
      scrollRef.current === null ||
      lastPixelOffsetKeyRef.current === validatedPixelOffsetRequest.key
    )
      return;
    const scrollOffset = validatedPixelOffsetRequest.offsetPx;
    scrollRef.current[virtualizedScrollOffsetProperty(horizontal)] = scrollOffset;
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

  const renderContent = (item: TItem | undefined, virtualIndex: number): ReactNode =>
    renderVirtualizedRow({
      empty,
      header,
      isFetchingNextPage,
      item,
      layout,
      loadingLabel,
      nextBoundary,
      previousBoundary,
      renderItem,
      virtualIndex,
    });
  const renderedRows = renderVirtualizedInfiniteListRows(
    fallbackIndexes,
    virtualItems,
    (fallbackIndex, virtualItem): ReactNode =>
      renderVirtualizedInfiniteListRow({
        fallbackIndex,
        getItemKey,
        getItemWrapperProps,
        itemRole,
        items,
        layout,
        measureElement: virtualizer.measureElement,
        orientation,
        paddingEnd,
        paddingStart,
        renderContent,
        rowSpacing,
        stickyItemKeys,
        virtualItem,
      }),
  );
  const hasHorizontalBoundary = resolveHorizontalBoundary(horizontal, layout, nextBoundary);
  const containerClassName = resolveVirtualizedContainerClassName(className, horizontal);
  if (count > 0 && virtualItems.length === 0) {
    return (
      <div
        aria-label={ariaLabel}
        className={containerClassName}
        data-testid={testId}
        id={id}
        onScroll={onScroll}
        ref={setScrollElement}
        role={role}
      >
        {renderedRows}
      </div>
    );
  }
  return (
    <div
      aria-label={ariaLabel}
      className={containerClassName}
      data-testid={testId}
      id={id}
      onScroll={onScroll}
      ref={setScrollElement}
      role={role}
    >
      <div
        className={resolveVirtualizedInnerClassName(horizontal, hasHorizontalBoundary)}
        style={resolveVirtualizedInnerStyle(horizontal, virtualizer.getTotalSize())}
      >
        {renderedRows}
      </div>
    </div>
  );
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
    const element = scrollRef.current;
    const canRestoreAnchor =
      previousEntries.length > 0 &&
      anchor !== null &&
      element !== null &&
      resolveAnchorEntryIndex(previousEntries, anchor) >= 0 &&
      resolveAnchorEntryIndex(currentEntries, anchor) >= 0;
    if (canRestoreAnchor) {
      const restore = () => {
        restoreLeadingAnchor({
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
        });
      };
      restore();
      if (orientation === "horizontal") {
        const window = element.ownerDocument.defaultView;
        if (window !== null) {
          const frame = window.requestAnimationFrame(restore);
          previousAnchorEntriesRef.current = currentEntries;
          pixelOffsetAppliedKeyRef.current = null;
          return () => {
            window.cancelAnimationFrame(frame);
          };
        }
      }
    }
    previousAnchorEntriesRef.current = currentEntries;
    pixelOffsetAppliedKeyRef.current = null;
    if (!canRestoreAnchor || orientation !== "horizontal") {
      captureLeadingAnchor();
    }
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
    const measuredOffset = virtualizer.getOffsetForIndex(virtualIndex, "start")?.[0];
    if (measuredOffset === undefined) {
      return;
    }
    const scrollOffset = measuredOffset - anchor.inRowOffset;
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
