import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  type CSSProperties,
  type ReactNode,
} from "react";
import { type Range, type VirtualItem, useVirtualizer } from "@tanstack/react-virtual";

import { cx } from "./classes";
import { resolveVirtualizedInitialScroll } from "./virtualizedInfiniteListInitialScroll";
import { useVirtualizedLoadMore } from "./virtualizedInfiniteListLoadMore";
import { pinnedVirtualRangeExtractor } from "./virtualizedPinnedRange";
import {
  requireVirtualizedPixelOffsetRequest,
  type VirtualizedPixelOffsetRequest,
} from "./virtualizedPixelOffsetRequest";
import { shouldAdjustScrollForVirtualizedResize } from "./virtualizedResizePolicy";

export type VirtualizedFrameEntry = Readonly<{
  key: string;
  anchorKey?: string | undefined;
  occurrenceKey?: string | undefined;
  render: () => ReactNode;
}>;

export type VirtualizedFrameLoadTrigger = Readonly<{
  key: string;
  isAtEdge: (visibleEntryKeys: readonly string[]) => boolean;
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  onLoadMore?: (() => void) | undefined;
}>;

export type VirtualizedFrameProps = Readonly<{
  entries: readonly VirtualizedFrameEntry[];
  estimateSize: () => number;
  loadTriggers?: readonly VirtualizedFrameLoadTrigger[] | undefined;
  role: "grid" | "list" | "listbox";
  rowRole: "listitem" | "presentation" | "row";
  rowSpacing: "default" | "compact" | "tight";
  id?: string | undefined;
  ariaLabel?: string | undefined;
  testId?: string | undefined;
  initialScrollKey?: string | undefined;
  initialScrollRequestKey?: string | undefined;
  initialScrollAlign: "auto" | "start";
  paddingEnd: number;
  paddingStart: number;
  className?: string | undefined;
  nonAdjustingResizeItemKey?: string | undefined;
  onScrollElementChange?: ((element: HTMLDivElement | null) => void) | undefined;
  pinnedItemKeys?: ReadonlySet<string> | undefined;
  pixelOffsetRequest?: VirtualizedPixelOffsetRequest | undefined;
  canApplyPixelOffset: boolean;
}>;

export function VirtualizedFrame({
  entries,
  estimateSize,
  loadTriggers = [],
  role,
  rowRole,
  rowSpacing,
  id,
  ariaLabel,
  testId,
  initialScrollKey,
  initialScrollRequestKey,
  initialScrollAlign,
  paddingEnd,
  paddingStart,
  className,
  nonAdjustingResizeItemKey,
  onScrollElementChange,
  pinnedItemKeys,
  pixelOffsetRequest,
  canApplyPixelOffset,
}: VirtualizedFrameProps) {
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
  const leadingAnchorRef = useRef<VirtualizedLeadingAnchor | null>(null);
  const previousAnchorEntriesRef = useRef<readonly VirtualizedAnchorEntry[]>([]);
  const pinnedIndexes = useMemo(() => {
    if (pinnedItemKeys === undefined || pinnedItemKeys.size === 0) {
      return new Set<number>();
    }
    const indexes = new Set<number>();
    entries.forEach((entry, index) => {
      if (pinnedItemKeys.has(entry.key)) indexes.add(index);
    });
    return indexes;
  }, [entries, pinnedItemKeys]);
  // TanStack Virtual is the single windowing owner. Adapters supply typed entries and edge triggers.
  // The react-hooks/incompatible-library check is scoped off for this module in eslint.config.js.
  const virtualizer = useVirtualizer({
    count: entries.length,
    getScrollElement: () => scrollRef.current,
    estimateSize,
    initialRect: { width: 800, height: 600 },
    paddingEnd,
    paddingStart,
    useFlushSync: false,
    getItemKey: (index) => entries[index]?.key ?? `missing-${index.toString()}`,
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
    count: entries.length,
    estimateSize,
    pinnedIndexes,
  });
  const visibleIndexes = isFallbackRendering
    ? fallbackIndexes
    : virtualItems.map((virtualItem) => virtualItem.index);
  const visibleEntryKeys = useMemo(
    () =>
      visibleIndexes.flatMap((index) => {
        const key = entries[index]?.key;
        return key === undefined ? [] : [key];
      }),
    [entries, visibleIndexes],
  );

  const captureLeadingAnchor = useCallback(() => {
    const element = scrollRef.current;
    const anchorIndexes = anchorEntryIndexes(entries);
    if (element === null || anchorIndexes.length === 0) {
      leadingAnchorRef.current = null;
      return;
    }
    const scrollTop = element.scrollTop;
    const virtualItem = virtualizer
      .getVirtualItems()
      .find(
        (item) =>
          entries[item.index]?.anchorKey !== undefined && item.start <= scrollTop && item.end > scrollTop,
      );
    const virtualIndex =
      virtualItem?.index ??
      closestAnchorIndex(
        anchorIndexes,
        Math.floor(Math.max(0, scrollTop - paddingStart) / Math.max(1, estimateSize())),
      );
    const entry = entries[virtualIndex];
    if (entry?.anchorKey === undefined) {
      leadingAnchorRef.current = null;
      return;
    }
    const rowStart = virtualItem?.start ?? paddingStart + virtualIndex * Math.max(1, estimateSize());
    leadingAnchorRef.current = {
      anchorKey: entry.anchorKey,
      occurrenceKey: entry.occurrenceKey ?? entry.key,
      inRowOffset: scrollTop - rowStart,
    };
  }, [entries, estimateSize, paddingStart, virtualizer]);

  useEffect(() => {
    const scroll = resolveVirtualizedInitialScroll({
      getItemKey: (entry: VirtualizedFrameEntry) => entry.key,
      headerCount: 0,
      initialScrollKey,
      initialScrollRequestKey,
      items: entries,
      lastRequestKey: lastInitialScrollKeyRef.current,
    });
    if (scroll === null) return;
    lastInitialScrollKeyRef.current = scroll.requestKey;
    virtualizer.scrollToIndex(scroll.scrollIndex, {
      align: initialScrollAlign,
      behavior: "auto",
    });
  }, [entries, initialScrollAlign, initialScrollKey, initialScrollRequestKey, virtualizer]);

  useLayoutEffect(() => {
    if (
      validatedPixelOffsetRequest === undefined ||
      !canApplyPixelOffset ||
      scrollRef.current === null ||
      lastPixelOffsetKeyRef.current === validatedPixelOffsetRequest.key
    ) {
      return;
    }
    scrollRef.current.scrollTop = validatedPixelOffsetRequest.offsetPx;
    virtualizer.scrollToOffset(scrollRef.current.scrollTop, { behavior: "auto" });
    lastPixelOffsetKeyRef.current = validatedPixelOffsetRequest.key;
    pixelOffsetAppliedKeyRef.current = validatedPixelOffsetRequest.key;
  }, [canApplyPixelOffset, validatedPixelOffsetRequest, virtualizer]);

  useLayoutEffect(() => {
    const currentEntries = entries.flatMap((entry, index) =>
      entry.anchorKey === undefined
        ? []
        : [
            {
              anchorKey: entry.anchorKey,
              occurrenceKey: entry.occurrenceKey ?? entry.key,
              virtualIndex: index,
            },
          ],
    );
    const previousEntries = previousAnchorEntriesRef.current;
    const anchor = recoverableLeadingAnchor(
      leadingAnchorRef.current,
      pixelOffsetAppliedKeyRef.current,
      validatedPixelOffsetRequest,
    );
    const element = scrollRef.current;
    if (previousEntries.length > 0 && anchor !== null && element !== null) {
      const previousEntry = resolveAnchorEntry(previousEntries, anchor);
      const currentEntry = resolveAnchorEntry(currentEntries, anchor);
      if (previousEntry !== undefined && currentEntry !== undefined) {
        const measuredOffset = isFallbackRendering
          ? undefined
          : virtualizer.getOffsetForIndex(currentEntry.virtualIndex, "start")?.[0];
        const rowOffset =
          measuredOffset ?? paddingStart + currentEntry.virtualIndex * Math.max(1, estimateSize());
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
    entries,
    estimateSize,
    isFallbackRendering,
    paddingStart,
    validatedPixelOffsetRequest,
    virtualItems,
    virtualizer,
  ]);

  if (entries.length > 0 && virtualItems.length === 0) {
    return (
      <VirtualizedFrameScrollport
        ariaLabel={ariaLabel}
        className={className}
        id={id}
        onScroll={captureLeadingAnchor}
        ref={setScrollElement}
        role={role}
        testId={testId}
      >
        {fallbackIndexes.map((index) => {
          const entry = entries[index];
          return entry === undefined ? null : (
            <div
              className={virtualRowClassName({
                count: entries.length,
                index,
                rowSpacing,
                virtualized: false,
              })}
              key={entry.key}
              role={rowRole}
              style={fallbackRowStyle({
                count: entries.length,
                index,
                paddingEnd,
                paddingStart,
              })}
            >
              {entry.render()}
            </div>
          );
        })}
        <VirtualizedFrameLoadTriggers triggers={loadTriggers} visibleEntryKeys={visibleEntryKeys} />
      </VirtualizedFrameScrollport>
    );
  }

  return (
    <VirtualizedFrameScrollport
      ariaLabel={ariaLabel}
      className={className}
      id={id}
      onScroll={captureLeadingAnchor}
      ref={setScrollElement}
      role={role}
      testId={testId}
    >
      <div className="relative w-full" style={{ height: `${virtualizer.getTotalSize().toString()}px` }}>
        {virtualItems.map((virtualItem) => {
          const entry = entries[virtualItem.index];
          return entry === undefined ? null : (
            <div
              className={cx(
                "absolute top-0 left-0 w-full",
                virtualRowClassName({
                  count: entries.length,
                  index: virtualItem.index,
                  rowSpacing,
                  virtualized: true,
                }),
              )}
              data-index={virtualItem.index}
              key={virtualItem.key}
              ref={virtualizer.measureElement}
              role={rowRole}
              style={{ transform: `translateY(${virtualItem.start.toString()}px)` }}
            >
              {entry.render()}
            </div>
          );
        })}
      </div>
      <VirtualizedFrameLoadTriggers triggers={loadTriggers} visibleEntryKeys={visibleEntryKeys} />
    </VirtualizedFrameScrollport>
  );
}

const VirtualizedFrameScrollport = ({
  ariaLabel,
  children,
  className,
  id,
  onScroll,
  ref,
  role,
  testId,
}: Readonly<{
  ariaLabel: string | undefined;
  children: ReactNode;
  className: string | undefined;
  id: string | undefined;
  onScroll: () => void;
  ref: (element: HTMLDivElement | null) => void;
  role: "grid" | "list" | "listbox";
  testId: string | undefined;
}>) => (
  <div
    aria-label={ariaLabel}
    className={className}
    data-testid={testId}
    id={id}
    onScroll={onScroll}
    ref={ref}
    role={role}
  >
    {children}
  </div>
);

function VirtualizedFrameLoadTriggers({
  triggers,
  visibleEntryKeys,
}: Readonly<{
  triggers: readonly VirtualizedFrameLoadTrigger[];
  visibleEntryKeys: readonly string[];
}>) {
  return triggers.map((trigger) => (
    <VirtualizedFrameLoadTriggerEffect
      key={trigger.key}
      trigger={trigger}
      visibleEntryKeys={visibleEntryKeys}
    />
  ));
}

function VirtualizedFrameLoadTriggerEffect({
  trigger,
  visibleEntryKeys,
}: Readonly<{
  trigger: VirtualizedFrameLoadTrigger;
  visibleEntryKeys: readonly string[];
}>) {
  const lastLoadMoreKeyRef = useRef<string | null>(null);
  const wasFetchingNextPageRef = useRef(false);
  useVirtualizedLoadMore({
    atEdge: trigger.isAtEdge(visibleEntryKeys),
    hasNextPage: trigger.hasNextPage,
    isFetchingNextPage: trigger.isFetchingNextPage,
    lastLoadMoreKeyRef,
    loadMoreKey: trigger.key,
    onLoadMore: trigger.onLoadMore,
    wasFetchingNextPageRef,
  });
  return null;
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
  return cx(virtualized ? index !== 0 && "pt-[var(--space-3)]" : "pt-[var(--space-3)] first:pt-0");
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
}>): CSSProperties | undefined {
  if (count === 0) return undefined;
  return {
    paddingBottom: index === count - 1 ? paddingEnd : undefined,
    paddingTop: index === 0 ? paddingStart : undefined,
  };
}

type VirtualizedLeadingAnchor = Readonly<{
  anchorKey: string;
  occurrenceKey: string;
  inRowOffset: number;
}>;

type VirtualizedAnchorEntry = Readonly<{
  anchorKey: string;
  occurrenceKey: string;
  virtualIndex: number;
}>;

function recoverableLeadingAnchor(
  anchor: VirtualizedLeadingAnchor | null,
  appliedPixelOffsetKey: string | null,
  request: VirtualizedPixelOffsetRequest | undefined,
): VirtualizedLeadingAnchor | null {
  return appliedPixelOffsetKey === request?.key ? null : anchor;
}

function resolveAnchorEntry(
  entries: readonly VirtualizedAnchorEntry[],
  anchor: VirtualizedLeadingAnchor,
): VirtualizedAnchorEntry | undefined {
  return (
    entries.find(
      (entry) => entry.anchorKey === anchor.anchorKey && entry.occurrenceKey === anchor.occurrenceKey,
    ) ?? entries.find((entry) => entry.anchorKey === anchor.anchorKey)
  );
}

function anchorEntryIndexes(entries: readonly VirtualizedFrameEntry[]): number[] {
  return entries.flatMap((entry, index) => (entry.anchorKey === undefined ? [] : [index]));
}

function closestAnchorIndex(anchorIndexes: readonly number[], estimatedIndex: number): number {
  for (let index = anchorIndexes.length - 1; index >= 0; index -= 1) {
    const anchorIndex = anchorIndexes[index];
    if (anchorIndex !== undefined && anchorIndex <= estimatedIndex) return anchorIndex;
  }
  return anchorIndexes[0] ?? estimatedIndex;
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
  if (count === 0) return [];
  const visibleCount = Math.max(1, Math.ceil(600 / Math.max(1, estimateSize())));
  const range: Range = {
    count,
    startIndex: 0,
    endIndex: Math.min(count - 1, visibleCount - 1),
    overscan: 6,
  };
  return pinnedVirtualRangeExtractor(range, pinnedIndexes);
}
