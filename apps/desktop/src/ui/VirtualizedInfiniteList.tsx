import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  type AriaRole,
  type HTMLAttributes,
  type ReactElement,
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
import { useVirtualizedLeadingAnchor } from "./virtualizedLeadingAnchor";
import {
  resolveVirtualizedInfiniteListLayout,
  renderVirtualizedInfiniteListRow,
  type VirtualizedInfiniteListLayout,
  type VirtualizedInfiniteListRowContext,
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
  const { count, headerIndex, itemStartIndex } = layout;
  const retainedItemKeys = useMemo(
    () => new Set([...(pinnedItemKeys ?? []), ...(stickyItemKeys ?? [])]),
    [pinnedItemKeys, stickyItemKeys],
  );
  const pinnedIndexes = useMemo(() => {
    const indexes = new Set<number>();
    if (horizontal && headerIndex !== null) {
      indexes.add(headerIndex);
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
  }, [getItemKey, headerIndex, horizontal, itemStartIndex, items, retainedItemKeys]);
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
  const fallbackIndexes = fallbackVirtualIndexes({
    count,
    estimateSize,
    pinnedIndexes,
  });
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

  useVirtualizedPixelOffset({
    horizontal,
    itemsLength: items.length,
    lastPixelOffsetKeyRef,
    pixelOffsetAppliedKeyRef,
    request: validatedPixelOffsetRequest,
    scrollRef,
    virtualizer,
  });

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

  const rowContext: VirtualizedInfiniteListRowContext<TItem> = {
    empty,
    getItemKey,
    getItemWrapperProps,
    header,
    isFetchingNextPage,
    itemRole,
    items,
    layout,
    loadingLabel,
    nextBoundary,
    orientation,
    paddingEnd,
    paddingStart,
    previousBoundary,
    renderItem,
    rowSpacing,
    stickyItemKeys,
  };

  return (
    <VirtualizedInfiniteListViewport
      className={className}
      fallbackIndexes={fallbackIndexes}
      id={id}
      onScroll={onScroll}
      ariaLabel={ariaLabel}
      rowContext={rowContext}
      setScrollElement={setScrollElement}
      role={role}
      testId={testId}
      virtualItems={virtualItems}
      virtualizer={virtualizer}
    />
  );
}

type VirtualizedInfiniteListViewportProps<TItem> = Readonly<{
  ariaLabel: string | undefined;
  className: string | undefined;
  fallbackIndexes: readonly number[];
  id: string | undefined;
  onScroll: (() => void) | undefined;
  rowContext: VirtualizedInfiniteListRowContext<TItem>;
  role: AriaRole;
  setScrollElement: (element: HTMLDivElement | null) => void;
  testId: string | undefined;
  virtualItems: readonly VirtualItem[];
  virtualizer: ReturnType<typeof useVirtualizer<HTMLDivElement, Element>>;
}>;

function VirtualizedInfiniteListViewport<TItem>({
  ariaLabel,
  className,
  fallbackIndexes,
  id,
  onScroll,
  rowContext,
  setScrollElement,
  role,
  testId,
  virtualItems,
  virtualizer,
}: VirtualizedInfiniteListViewportProps<TItem>): ReactElement {
  const { layout, orientation } = rowContext;
  const horizontal = orientation === "horizontal";
  const hasHorizontalBoundary =
    horizontal &&
    (layout.emptyCount > 0 ||
      layout.previousBoundaryIndex !== null ||
      rowContext.nextBoundary !== undefined ||
      layout.legacyPlaceholderIndex !== null);
  if (layout.count > 0 && virtualItems.length === 0) {
    return (
      <div
        aria-label={ariaLabel}
        className={cx(className, horizontal && "flex")}
        data-testid={testId}
        id={id}
        onScroll={onScroll}
        ref={setScrollElement}
        role={role}
      >
        {fallbackIndexes.map((index) =>
          renderVirtualizedInfiniteListRow({
            context: rowContext,
            index,
            measureElement: undefined,
          }),
        )}
      </div>
    );
  }
  return (
    <div
      aria-label={ariaLabel}
      className={cx(className, horizontal && "flex")}
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
        {virtualItems.map((virtualItem) =>
          renderVirtualizedInfiniteListRow({
            context: rowContext,
            measureElement: virtualizer.measureElement,
            virtualItem,
          }),
        )}
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

function useVirtualizedPixelOffset({
  horizontal,
  itemsLength,
  lastPixelOffsetKeyRef,
  pixelOffsetAppliedKeyRef,
  request,
  scrollRef,
  virtualizer,
}: Readonly<{
  horizontal: boolean;
  itemsLength: number;
  lastPixelOffsetKeyRef: { current: string | null };
  pixelOffsetAppliedKeyRef: { current: string | null };
  request: VirtualizedPixelOffsetRequest | undefined;
  scrollRef: { current: HTMLDivElement | null };
  virtualizer: ReturnType<typeof useVirtualizer<HTMLDivElement, Element>>;
}>): void {
  useLayoutEffect(() => {
    if (
      request === undefined ||
      itemsLength === 0 ||
      scrollRef.current === null ||
      lastPixelOffsetKeyRef.current === request.key
    ) {
      return;
    }
    const scrollOffset = request.offsetPx;
    if (horizontal) {
      scrollRef.current.scrollLeft = scrollOffset;
    } else {
      scrollRef.current.scrollTop = scrollOffset;
    }
    virtualizer.scrollToOffset(scrollOffset, { behavior: "auto" });
    lastPixelOffsetKeyRef.current = request.key;
    pixelOffsetAppliedKeyRef.current = request.key;
  }, [
    horizontal,
    itemsLength,
    lastPixelOffsetKeyRef,
    pixelOffsetAppliedKeyRef,
    request,
    scrollRef,
    virtualizer,
  ]);
}
