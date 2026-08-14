import type { CSSProperties, ReactNode } from "react";

import { cx } from "./classes";
import { InfiniteListBoundary, type VirtualizedInfiniteListBoundaryState } from "./InfiniteListBoundary";
import { Spinner } from "./Spinner";

export function fallbackVirtualizedRowStyle({
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
  if (count === 0) {
    return undefined;
  }
  return {
    paddingBottom: index === count - 1 ? paddingEnd : undefined,
    paddingTop: index === 0 ? paddingStart : undefined,
  };
}

export function virtualizedRowClassName({
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

export function virtualizedRowKey<TItem>({
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

export function renderVirtualizedRow<TItem>({
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
