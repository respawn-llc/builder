import type { CSSProperties, ReactNode } from "react";

import { cx } from "./classes";
import { InfiniteListBoundary, type VirtualizedInfiniteListBoundaryState } from "./InfiniteListBoundary";
import { Spinner } from "./Spinner";

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
  emptyCount,
  getItemKey,
  headerCount,
  horizontal,
  index,
  itemStartIndex,
  items,
  nextBoundaryIndex,
  previousBoundaryCount,
}: Readonly<{
  emptyCount: number;
  getItemKey: (item: TItem) => string;
  headerCount: number;
  horizontal: boolean;
  index: number;
  itemStartIndex: number;
  items: readonly TItem[];
  nextBoundaryIndex: number;
  previousBoundaryCount: number;
}>): string {
  if (previousBoundaryCount > 0 && index === (horizontal ? headerCount : 0)) {
    return "boundary-previous";
  }
  if (headerCount > 0 && index === (horizontal ? 0 : previousBoundaryCount)) {
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

export function renderVirtualizedRow<TItem>({
  empty,
  emptyCount,
  header,
  headerCount,
  horizontal,
  isFetchingNextPage,
  item,
  itemStartIndex,
  legacyPlaceholderIndex,
  loadingLabel,
  nextBoundary,
  nextBoundaryIndex,
  previousBoundary,
  previousBoundaryCount,
  renderItem,
  virtualIndex,
}: Readonly<{
  empty: ReactNode | undefined;
  emptyCount: number;
  header: ReactNode | undefined;
  headerCount: number;
  horizontal: boolean;
  isFetchingNextPage: boolean;
  item: TItem | undefined;
  itemStartIndex: number;
  legacyPlaceholderIndex: number | null;
  loadingLabel: string;
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  nextBoundaryIndex: number | null;
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  previousBoundaryCount: number;
  renderItem: (item: TItem, itemIndex: number) => ReactNode;
  virtualIndex: number;
}>): ReactNode {
  const previousBoundaryIndex = headerCount * Number(horizontal);
  const headerIndex = previousBoundaryCount * Number(!horizontal);
  if (previousBoundary !== undefined && virtualIndex === previousBoundaryIndex) {
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
  return item === undefined ? null : renderItem(item, virtualIndex - itemStartIndex);
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
