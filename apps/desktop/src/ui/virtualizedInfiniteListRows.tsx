import type { AriaRole, CSSProperties, HTMLAttributes, ReactElement, ReactNode } from "react";
import type { VirtualItem } from "@tanstack/react-virtual";

import { cx } from "./classes";
import { InfiniteListBoundary, type VirtualizedInfiniteListBoundaryState } from "./InfiniteListBoundary";
import { Spinner } from "./Spinner";

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
  const previousBoundaryCount = Number(previousBoundary !== undefined && hasHorizontalContent);
  const headerCount = Number(header !== undefined);
  const emptyCount = Number(itemsLength === 0 && empty !== undefined);
  const itemStartIndex = previousBoundaryCount + headerCount;
  const nextBoundaryIndex = itemStartIndex + Math.max(itemsLength, emptyCount);
  const nextBoundaryCount = Number(nextBoundary !== undefined && hasHorizontalContent);
  const legacyPlaceholderIndex =
    nextBoundary === undefined && hasNextPage && hasHorizontalContent ? nextBoundaryIndex : null;
  const count = nextBoundaryIndex + nextBoundaryCount + Number(legacyPlaceholderIndex !== null);
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

export type VirtualizedInfiniteListRowContext<TItem> = Readonly<{
  empty: ReactNode | undefined;
  getItemKey: (item: TItem) => string;
  getItemWrapperProps: ((item: TItem, itemIndex: number) => HTMLAttributes<HTMLDivElement>) | undefined;
  header: ReactNode | undefined;
  isFetchingNextPage: boolean;
  itemRole: AriaRole;
  items: readonly TItem[];
  layout: VirtualizedInfiniteListLayout;
  loadingLabel: string;
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  orientation: "vertical" | "horizontal";
  paddingEnd: number;
  paddingStart: number;
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  renderItem: (item: TItem, itemIndex: number) => ReactNode;
  rowSpacing: "default" | "compact" | "tight";
  stickyItemKeys: ReadonlySet<string> | undefined;
}>;

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
  layout,
  getItemKey,
  index,
  items,
}: Readonly<{
  layout: VirtualizedInfiniteListLayout;
  getItemKey: (item: TItem) => string;
  index: number;
  items: readonly TItem[];
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

export function renderVirtualizedInfiniteListRow<TItem>({
  context,
  index,
  measureElement,
  virtualItem,
}: Readonly<{
  context: VirtualizedInfiniteListRowContext<TItem>;
  index?: number;
  measureElement: ((element: Element | null) => void) | undefined;
  virtualItem?: VirtualItem;
}>): ReactElement | null {
  const row = resolveVirtualizedInfiniteListRow({ context, index, virtualItem });
  if (row === null) {
    return null;
  }
  const { layout, orientation, paddingEnd, paddingStart, rowSpacing, itemRole } = context;
  return (
    <div
      {...row.wrapperProps}
      className={cx(
        virtualizedRowPositionClassName({
          horizontal: row.horizontal,
          sticky: row.sticky,
          virtualized: virtualItem !== undefined,
        }),
        virtualizedRowClassName({
          count: layout.count,
          index: row.virtualIndex,
          orientation,
          rowSpacing,
          virtualized: virtualItem !== undefined,
        }),
        row.boundary && "min-w-64",
        row.wrapperProps?.className,
      )}
      data-index={virtualItem?.index}
      key={row.key}
      ref={virtualItem === undefined ? undefined : measureElement}
      role={itemRole}
      style={virtualizedRowStyle({
        count: layout.count,
        index: row.virtualIndex,
        orientation,
        paddingEnd,
        paddingStart,
        sticky: row.sticky,
        virtualItem,
        wrapperStyle: row.wrapperProps?.style,
      })}
    >
      {row.content}
    </div>
  );
}

type ResolvedVirtualizedInfiniteListRow = Readonly<{
  boundary: boolean;
  content: ReactNode;
  horizontal: boolean;
  key: string;
  sticky: boolean;
  virtualIndex: number;
  wrapperProps: HTMLAttributes<HTMLDivElement> | undefined;
}>;

function resolveVirtualizedInfiniteListRow<TItem>({
  context,
  index,
  virtualItem,
}: Readonly<{
  context: VirtualizedInfiniteListRowContext<TItem>;
  index: number | undefined;
  virtualItem: VirtualItem | undefined;
}>): ResolvedVirtualizedInfiniteListRow | null {
  const virtualIndex = virtualItem?.index ?? index;
  if (virtualIndex === undefined) {
    return null;
  }
  const { getItemKey, getItemWrapperProps, items, layout, orientation, stickyItemKeys } = context;
  const itemIndex = virtualIndex - layout.itemStartIndex;
  const item = items[itemIndex];
  const itemKey = item === undefined ? undefined : getItemKey(item);
  const wrapperProps = item === undefined ? undefined : getItemWrapperProps?.(item, itemIndex);
  const { boundary, horizontal, sticky } = resolveVirtualizedRowGeometry({
    itemKey,
    layout,
    orientation,
    stickyItemKeys,
    virtualIndex,
  });
  const content = renderVirtualizedRowContent({
    context,
    item,
    itemIndex,
    virtualIndex,
  });
  return {
    boundary,
    content,
    horizontal,
    key: virtualItem?.key.toString() ?? virtualizedRowKey({ layout, getItemKey, index: virtualIndex, items }),
    sticky,
    virtualIndex,
    wrapperProps,
  };
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
}>): Readonly<{ boundary: boolean; horizontal: boolean; sticky: boolean }> {
  const horizontal = orientation === "horizontal";
  const sticky =
    (horizontal && layout.headerIndex === virtualIndex) ||
    (itemKey !== undefined && stickyItemKeys?.has(itemKey) === true);
  const boundary =
    horizontal &&
    (layout.previousBoundaryIndex === virtualIndex ||
      layout.nextBoundaryIndex === virtualIndex ||
      layout.legacyPlaceholderIndex === virtualIndex ||
      (layout.emptyCount > 0 && layout.itemStartIndex === virtualIndex));
  return { boundary, horizontal, sticky };
}

function renderVirtualizedRowContent<TItem>({
  context,
  item,
  itemIndex,
  virtualIndex,
}: Readonly<{
  context: VirtualizedInfiniteListRowContext<TItem>;
  item: TItem | undefined;
  itemIndex: number;
  virtualIndex: number;
}>): ReactNode {
  const {
    empty,
    header,
    isFetchingNextPage,
    layout,
    loadingLabel,
    nextBoundary,
    previousBoundary,
    renderItem,
  } = context;
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
  return item === undefined ? null : renderItem(item, itemIndex);
}

function virtualizedRowPositionClassName({
  horizontal,
  sticky,
  virtualized,
}: Readonly<{ horizontal: boolean; sticky: boolean; virtualized: boolean }>): string | undefined {
  if (!virtualized) {
    return undefined;
  }
  if (sticky) {
    return horizontal ? "sticky z-[1] w-max" : "sticky top-0 z-[1] w-full";
  }
  return horizontal ? "absolute top-0 left-0 w-max" : "absolute top-0 left-0 w-full";
}

function virtualizedRowStyle({
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
