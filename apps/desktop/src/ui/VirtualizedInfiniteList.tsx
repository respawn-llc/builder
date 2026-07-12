import { useEffect, useRef, type ReactNode } from "react";
import { type VirtualItem, useVirtualizer } from "@tanstack/react-virtual";

import { cx } from "./classes";
import { Spinner } from "./Spinner";
import { resolveVirtualizedInitialScroll } from "./virtualizedInfiniteListInitialScroll";
import { resolveLoadMore } from "./virtualizedInfiniteListLoadMore";
import { shouldAdjustScrollForVirtualizedResize } from "./virtualizedResizePolicy";

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
}: VirtualizedInfiniteListProps<TItem>) {
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const lastInitialScrollKeyRef = useRef("");
  const lastLoadMoreKeyRef = useRef("");
  const wasFetchingNextPageRef = useRef(false);
  const headerCount = header === undefined ? 0 : 1;
  const emptyCount = items.length === 0 && empty !== undefined ? 1 : 0;
  const placeholderCount = hasNextPage ? 1 : 0;
  const count = headerCount + Math.max(items.length, emptyCount) + placeholderCount;
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
      if (header !== undefined && index === 0) {
        return "header";
      }
      if (items.length === 0 && empty !== undefined && index === headerCount) {
        return "empty";
      }
      const item = items[index - headerCount];
      return item === undefined ? `placeholder-${index.toString()}` : getItemKey(item);
    },
    overscan: 6,
  });
  virtualizer.shouldAdjustScrollPositionOnItemSizeChange =
    nonAdjustingResizeItemKey === undefined
      ? undefined
      : (item: VirtualItem) => shouldAdjustScrollForVirtualizedResize(nonAdjustingResizeItemKey, String(item.key));
  const virtualItems = virtualizer.getVirtualItems();
  const renderRow = (virtualIndex: number): ReactNode =>
    renderVirtualRow({
      empty,
      emptyCount,
      header,
      headerCount,
      isFetchingNextPage,
      item: items[virtualIndex - headerCount],
      loadingLabel,
      renderItem,
      virtualIndex,
    });

  useEffect(() => {
    if (initialScrollKey === undefined || initialScrollKey.length === 0) {
      return;
    }
    const scroll = resolveVirtualizedInitialScroll({
      getItemKey,
      headerCount,
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
  }, [getItemKey, headerCount, initialScrollKey, initialScrollRequestKey, items, virtualizer]);

  useEffect(() => {
    const lastItem = virtualItems.at(-1);
    const lastDataIndex = headerCount + items.length - 1;
    const decision = resolveLoadMore({
      atBottom: lastItem !== undefined && lastItem.index >= lastDataIndex,
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
  }, [hasNextPage, headerCount, isFetchingNextPage, items.length, loadMoreKey, onLoadMore, virtualItems]);

  if (count > 0 && virtualItems.length === 0) {
    return (
      <div aria-label={ariaLabel} className={className} data-testid={testId} ref={scrollRef} role="list">
        {Array.from({ length: count }, (_value, index) => (
          <div
            className={virtualRowClassName({ count, index, rowSpacing, virtualized: false })}
            key={fallbackRowKey({ emptyCount, getItemKey, headerCount, index, items })}
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
    <div aria-label={ariaLabel} className={className} data-testid={testId} ref={scrollRef} role="list">
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
  return cx(
    virtualized ? index !== 0 && "pt-[var(--space-3)]" : "pt-[var(--space-3)] first:pt-0",
  );
}

function fallbackRowKey<TItem>({
  emptyCount,
  getItemKey,
  headerCount,
  index,
  items,
}: Readonly<{
  emptyCount: number;
  getItemKey: (item: TItem) => string;
  headerCount: number;
  index: number;
  items: readonly TItem[];
}>): string {
  if (headerCount > 0 && index === 0) {
    return "header";
  }
  if (emptyCount > 0 && index === headerCount) {
    return "empty";
  }
  const item = items[index - headerCount];
  return item === undefined ? `placeholder-${index.toString()}` : getItemKey(item);
}

function renderVirtualRow<TItem>({
  empty,
  emptyCount,
  header,
  headerCount,
  isFetchingNextPage,
  item,
  loadingLabel,
  renderItem,
  virtualIndex,
}: Readonly<{
  empty: ReactNode | undefined;
  emptyCount: number;
  header: ReactNode | undefined;
  headerCount: number;
  isFetchingNextPage: boolean;
  item: TItem | undefined;
  loadingLabel: string;
  renderItem: (item: TItem) => ReactNode;
  virtualIndex: number;
}>): ReactNode {
  if (header !== undefined && virtualIndex === 0) {
    return header;
  }
  if (emptyCount > 0 && virtualIndex === headerCount) {
    return empty;
  }
  if (item === undefined) {
    return <VirtualizedPlaceholder loading={isFetchingNextPage} loadingLabel={loadingLabel} />;
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
