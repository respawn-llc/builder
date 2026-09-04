import type { AriaRole, CSSProperties, HTMLAttributes, ReactNode } from "react";
import { type Range, type VirtualItem } from "@tanstack/react-virtual";

import { cx } from "./classes";
import { InfiniteListBoundary, type VirtualizedInfiniteListBoundaryState } from "./InfiniteListBoundary";
import { Spinner } from "./Spinner";
import { pinnedVirtualRangeExtractor } from "./virtualizedPinnedRange";

export type VirtualizedInfiniteListLayout = Readonly<{
  count: number;
  emptyCount: number;
  headerIndex: number | null;
  itemStartIndex: number;
  legacyPlaceholderIndex: number | null;
  nextBoundaryIndex: number;
  previousBoundaryIndex: number | null;
}>;

export function resolveVirtualizedInfiniteListLayout({
  empty,
  hasNextPage,
  header,
  horizontal,
  itemsLength,
  nextBoundary,
  previousBoundary,
}: Readonly<{
  empty: ReactNode | undefined;
  hasNextPage: boolean;
  header: ReactNode | undefined;
  horizontal: boolean;
  itemsLength: number;
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
}>): VirtualizedInfiniteListLayout {
  const hasItems = itemsLength > 0;
  const hasHorizontalContent = !horizontal || hasItems;
  const previousBoundaryCount = optionalBoundaryCount(previousBoundary, hasHorizontalContent);
  const headerCount = header === undefined ? 0 : 1;
  const emptyCount = itemsLength === 0 && empty !== undefined ? 1 : 0;
  const itemStartIndex = previousBoundaryCount + headerCount;
  const nextBoundaryIndex = itemStartIndex + Math.max(itemsLength, emptyCount);
  const nextBoundaryCount = optionalBoundaryCount(nextBoundary, hasHorizontalContent);
  const legacyPlaceholderIndex = resolveLegacyPlaceholderIndex(
    nextBoundary,
    hasNextPage,
    hasHorizontalContent,
    nextBoundaryIndex,
  );
  const count = nextBoundaryIndex + nextBoundaryCount + (legacyPlaceholderIndex === null ? 0 : 1);
  return {
    count,
    emptyCount,
    headerIndex: header === undefined ? null : horizontal ? 0 : previousBoundaryCount,
    itemStartIndex,
    legacyPlaceholderIndex,
    nextBoundaryIndex,
    previousBoundaryIndex: previousBoundaryCount === 0 ? null : horizontal ? headerCount : 0,
  };
}

function optionalBoundaryCount(
  boundary: VirtualizedInfiniteListBoundaryState | undefined,
  hasHorizontalContent: boolean,
): number {
  return boundary !== undefined && hasHorizontalContent ? 1 : 0;
}

function resolveLegacyPlaceholderIndex(
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined,
  hasNextPage: boolean,
  hasHorizontalContent: boolean,
  nextBoundaryIndex: number,
): number | null {
  return nextBoundary === undefined && hasNextPage && hasHorizontalContent ? nextBoundaryIndex : null;
}

export function fallbackVirtualizedRowStyle({
  count,
  index,
  orientation,
  paddingEnd,
  paddingStart,
}: Readonly<{
  count: number;
  index: number;
  orientation: "vertical" | "horizontal";
  paddingEnd: number;
  paddingStart: number;
}>): CSSProperties | undefined {
  if (count === 0) {
    return undefined;
  }
  if (orientation === "horizontal") {
    return {
      paddingLeft: index === 0 ? paddingStart : undefined,
      paddingRight: index === count - 1 ? paddingEnd : undefined,
    };
  }
  return {
    paddingBottom: index === count - 1 ? paddingEnd : undefined,
    paddingTop: index === 0 ? paddingStart : undefined,
  };
}

export function virtualizedRowClassName({
  count,
  index,
  orientation,
  rowSpacing,
  virtualized,
}: Readonly<{
  count: number;
  index: number;
  orientation: "vertical" | "horizontal";
  rowSpacing: "default" | "compact" | "tight";
  virtualized: boolean;
}>): string {
  if (orientation === "horizontal") {
    return index === count - 1 ? "shrink-0" : "shrink-0 pr-[var(--space-2)]";
  }
  if (rowSpacing === "compact") {
    return cx("pb-[var(--space-2)]", index === count - 1 && "pb-0");
  }
  if (rowSpacing === "tight") {
    return cx("pb-[var(--space-1)]", index === count - 1 && "pb-0");
  }
  return cx(virtualized ? index !== 0 && "pt-[var(--space-3)]" : "pt-[var(--space-3)] first:pt-0");
}

export function virtualizedRowKey<TItem>({
  getItemKey,
  index,
  items,
  layout,
}: Readonly<{
  getItemKey: (item: TItem) => string;
  index: number;
  items: readonly TItem[];
  layout: VirtualizedInfiniteListLayout;
}>): string {
  if (layout.previousBoundaryIndex !== null && index === layout.previousBoundaryIndex) {
    return "boundary-previous";
  }
  if (layout.headerIndex !== null && index === layout.headerIndex) {
    return "header";
  }
  if (layout.emptyCount > 0 && index === layout.itemStartIndex) {
    return "empty";
  }
  const item = items[index - layout.itemStartIndex];
  if (item !== undefined) {
    return getItemKey(item);
  }
  return layout.nextBoundaryIndex === index ? "boundary-next" : `placeholder-${index.toString()}`;
}

export function renderVirtualizedRow<TItem>({
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
}: Readonly<{
  empty: ReactNode | undefined;
  header: ReactNode | undefined;
  isFetchingNextPage: boolean;
  item: TItem | undefined;
  layout: VirtualizedInfiniteListLayout;
  loadingLabel: string;
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  renderItem: (item: TItem, itemIndex: number) => ReactNode;
  virtualIndex: number;
}>): ReactNode {
  if (previousBoundary !== undefined && virtualIndex === layout.previousBoundaryIndex) {
    return <InfiniteListBoundary direction="previous" state={previousBoundary} />;
  }
  if (header !== undefined && virtualIndex === layout.headerIndex) {
    return header;
  }
  if (layout.emptyCount > 0 && virtualIndex === layout.itemStartIndex) {
    return empty;
  }
  if (nextBoundary !== undefined && virtualIndex === layout.nextBoundaryIndex) {
    return <InfiniteListBoundary direction="next" state={nextBoundary} />;
  }
  if (layout.legacyPlaceholderIndex === virtualIndex) {
    return <VirtualizedPlaceholder loading={isFetchingNextPage} loadingLabel={loadingLabel} />;
  }
  return item === undefined ? null : renderItem(item, virtualIndex - layout.itemStartIndex);
}

export function renderVirtualizedInfiniteListRow<TItem>({
  fallbackIndex,
  getItemKey,
  getItemWrapperProps,
  itemRole,
  items,
  layout,
  measureElement,
  orientation,
  paddingEnd,
  paddingStart,
  renderContent,
  rowSpacing,
  stickyItemKeys,
  virtualItem,
}: Readonly<{
  fallbackIndex: number;
  getItemKey: (item: TItem) => string;
  getItemWrapperProps: ((item: TItem, itemIndex: number) => HTMLAttributes<HTMLDivElement>) | undefined;
  itemRole: AriaRole;
  items: readonly TItem[];
  layout: VirtualizedInfiniteListLayout;
  measureElement: ((element: Element | null) => void) | undefined;
  orientation: "vertical" | "horizontal";
  paddingEnd: number;
  paddingStart: number;
  renderContent: (item: TItem | undefined, virtualIndex: number) => ReactNode;
  rowSpacing: "default" | "compact" | "tight";
  stickyItemKeys: ReadonlySet<string> | undefined;
  virtualItem: VirtualItem | undefined;
}>): ReactNode {
  const virtualIndex = virtualItem?.index ?? fallbackIndex;
  const virtualized = virtualItem !== undefined;
  const { item, itemKey, wrapperProps } = resolveVirtualizedRowItem({
    getItemKey,
    getItemWrapperProps,
    items,
    itemStartIndex: layout.itemStartIndex,
    virtualIndex,
  });
  const { boundary, sticky } = resolveVirtualizedRowGeometry({
    itemKey,
    layout,
    orientation,
    stickyItemKeys,
    virtualIndex,
  });
  return (
    <div
      {...wrapperProps}
      className={cx(
        virtualizedRowPositionClassName({
          orientation,
          sticky,
          virtualized,
        }),
        virtualizedRowClassName({
          count: layout.count,
          index: virtualIndex,
          orientation,
          rowSpacing,
          virtualized,
        }),
        boundary ? "min-w-64" : undefined,
        wrapperProps?.className,
      )}
      data-index={virtualItem?.index}
      key={virtualItem?.key ?? virtualizedRowKey({ getItemKey, index: virtualIndex, items, layout })}
      ref={virtualized ? measureElement : undefined}
      role={itemRole}
      style={resolveVirtualizedRowStyle({
        count: layout.count,
        index: virtualIndex,
        orientation,
        paddingEnd,
        paddingStart,
        sticky,
        virtualItem,
        wrapperStyle: wrapperProps?.style,
      })}
    >
      {renderContent(item, virtualIndex)}
    </div>
  );
}

function resolveVirtualizedRowItem<TItem>({
  getItemKey,
  getItemWrapperProps,
  items,
  itemStartIndex,
  virtualIndex,
}: Readonly<{
  getItemKey: (item: TItem) => string;
  getItemWrapperProps: ((item: TItem, itemIndex: number) => HTMLAttributes<HTMLDivElement>) | undefined;
  items: readonly TItem[];
  itemStartIndex: number;
  virtualIndex: number;
}>): Readonly<{
  item: TItem | undefined;
  itemKey: string | undefined;
  wrapperProps: HTMLAttributes<HTMLDivElement> | undefined;
}> {
  const itemIndex = virtualIndex - itemStartIndex;
  const item = items[itemIndex];
  if (item === undefined) {
    return { item, itemKey: undefined, wrapperProps: undefined };
  }
  return {
    item,
    itemKey: getItemKey(item),
    wrapperProps: getItemWrapperProps?.(item, itemIndex),
  };
}

export function renderVirtualizedInfiniteListRows(
  fallbackIndexes: readonly number[],
  virtualItems: readonly VirtualItem[],
  renderRow: (fallbackIndex: number, virtualItem: VirtualItem | undefined) => ReactNode,
): ReactNode[] {
  if (virtualItems.length === 0) {
    return fallbackIndexes.map((fallbackIndex): ReactNode => renderRow(fallbackIndex, undefined));
  }
  return virtualItems.map((virtualItem): ReactNode => renderRow(virtualItem.index, virtualItem));
}

export function fallbackVirtualIndexes({
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

export function virtualizedVisibleIndexes(
  fallbackIndexes: readonly number[],
  virtualItems: readonly VirtualItem[],
): readonly number[] {
  return virtualItems.length === 0 ? fallbackIndexes : virtualItems.map((virtualItem) => virtualItem.index);
}

export function virtualizedScrollOffsetProperty(horizontal: boolean): "scrollLeft" | "scrollTop" {
  return horizontal ? "scrollLeft" : "scrollTop";
}

export function resolveHorizontalBoundary(
  horizontal: boolean,
  layout: VirtualizedInfiniteListLayout,
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined,
): boolean {
  const hasNextBoundary = nextBoundary !== undefined && layout.nextBoundaryIndex < layout.count;
  return horizontal && (layout.emptyCount > 0 || layout.previousBoundaryIndex !== null || hasNextBoundary);
}

export function resolveVirtualizedContainerClassName(
  className: string | undefined,
  horizontal: boolean,
): string {
  return cx(className, horizontal ? "flex" : undefined);
}

export function resolveVirtualizedInnerClassName(
  horizontal: boolean,
  hasHorizontalBoundary: boolean,
): string {
  return horizontal
    ? cx("relative w-max shrink-0", hasHorizontalBoundary ? "min-h-12" : "min-h-7")
    : "relative w-full";
}

export function resolveVirtualizedInnerStyle(
  horizontal: boolean,
  totalSize: number,
): Readonly<{ height?: string; width?: string }> {
  return horizontal ? { width: `${totalSize.toString()}px` } : { height: `${totalSize.toString()}px` };
}

function virtualizedRowPositionClassName({
  orientation,
  sticky,
  virtualized,
}: Readonly<{
  orientation: "vertical" | "horizontal";
  sticky: boolean;
  virtualized: boolean;
}>): string | undefined {
  if (!virtualized) {
    return undefined;
  }
  if (sticky) {
    return orientation === "horizontal" ? "sticky z-[1] w-max" : "sticky top-0 z-[1] w-full";
  }
  return orientation === "horizontal" ? "absolute top-0 left-0 w-max" : "absolute top-0 left-0 w-full";
}

function resolveVirtualizedRowGeometry({
  itemKey,
  layout,
  orientation,
  stickyItemKeys,
  virtualIndex,
}: Readonly<{
  itemKey: string | undefined;
  layout: VirtualizedInfiniteListLayout;
  orientation: "vertical" | "horizontal";
  stickyItemKeys: ReadonlySet<string> | undefined;
  virtualIndex: number;
}>): Readonly<{ boundary: boolean; sticky: boolean }> {
  const horizontal = orientation === "horizontal";
  const sticky = itemKey !== undefined && stickyItemKeys?.has(itemKey) === true;
  const boundary =
    horizontal &&
    (layout.previousBoundaryIndex === virtualIndex ||
      layout.nextBoundaryIndex === virtualIndex ||
      layout.legacyPlaceholderIndex === virtualIndex ||
      (layout.emptyCount > 0 && layout.itemStartIndex === virtualIndex));
  return { boundary, sticky };
}

function resolveVirtualizedRowStyle({
  count,
  index,
  orientation,
  paddingEnd,
  paddingStart,
  sticky,
  virtualItem,
  wrapperStyle,
}: Readonly<{
  count: number;
  index: number;
  orientation: "vertical" | "horizontal";
  paddingEnd: number;
  paddingStart: number;
  sticky: boolean;
  virtualItem: VirtualItem | undefined;
  wrapperStyle: CSSProperties | undefined;
}>): CSSProperties | undefined {
  if (virtualItem === undefined) {
    return fallbackVirtualizedRowStyle({
      count,
      index,
      orientation,
      paddingEnd,
      paddingStart,
    });
  }
  if (sticky) {
    return {
      ...wrapperStyle,
      ...(orientation === "horizontal" ? { left: paddingStart } : {}),
    };
  }
  return {
    ...wrapperStyle,
    transform:
      orientation === "horizontal"
        ? `translateX(${virtualItem.start.toString()}px)`
        : `translateY(${virtualItem.start.toString()}px)`,
  };
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
