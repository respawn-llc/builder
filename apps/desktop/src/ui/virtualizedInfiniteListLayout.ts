import type { ReactNode } from "react";

import type { VirtualizedInfiniteListBoundaryState } from "./InfiniteListBoundary";

export type VirtualizedListLayout = Readonly<{
  count: number;
  emptyIndex: number;
  emptyCount: number;
  itemStartIndex: number;
  legacyPlaceholderIndex: number | null;
  nextBoundaryIndex: number | null;
  previousBoundaryIndex: number | null;
  previousBoundaryCount: number;
}>;

export function virtualizedListLayout<TItem>({
  empty,
  getItemKey,
  hasHeader,
  hasNextPage,
  itemCount,
  items,
  nextBoundary,
  previousBoundary,
  previousLoadItemKey,
}: Readonly<{
  empty: ReactNode | undefined;
  getItemKey: (item: TItem) => string;
  hasHeader: boolean;
  hasNextPage: boolean;
  itemCount: number;
  items: readonly TItem[];
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  previousLoadItemKey: string | undefined;
}>): VirtualizedListLayout {
  const previousBoundaryCount = previousBoundary === undefined ? 0 : 1;
  const headerCount = hasHeader ? 1 : 0;
  const emptyCount = itemCount === 0 && empty !== undefined ? 1 : 0;
  const itemStartIndex = headerCount;
  const previousBoundaryIndex = resolvePreviousBoundaryIndex({
    getItemKey,
    itemStartIndex,
    items,
    previousBoundary,
    previousLoadItemKey,
  });
  const contentCount = Math.max(itemCount, emptyCount);
  const emptyIndex = resolveEmptyIndex(emptyCount, itemStartIndex, previousBoundaryIndex);
  const nextBoundaryCount = nextBoundary === undefined ? 0 : 1;
  const placeholderCount = nextBoundary === undefined && hasNextPage ? 1 : 0;
  const nextContentIndex = itemStartIndex + contentCount + previousBoundaryCount;
  return {
    count: nextContentIndex + nextBoundaryCount + placeholderCount,
    emptyIndex,
    emptyCount,
    itemStartIndex,
    legacyPlaceholderIndex: nextBoundary === undefined && hasNextPage ? nextContentIndex : null,
    nextBoundaryIndex: nextBoundary === undefined ? null : nextContentIndex,
    previousBoundaryIndex,
    previousBoundaryCount,
  };
}

function resolvePreviousBoundaryIndex<TItem>({
  getItemKey,
  itemStartIndex,
  items,
  previousBoundary,
  previousLoadItemKey,
}: Readonly<{
  getItemKey: (item: TItem) => string;
  itemStartIndex: number;
  items: readonly TItem[];
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  previousLoadItemKey: string | undefined;
}>): number | null {
  if (previousBoundary === undefined) {
    return null;
  }
  if (previousLoadItemKey === undefined) {
    return 0;
  }
  const itemIndex = items.findIndex((item) => getItemKey(item) === previousLoadItemKey);
  return itemStartIndex + Math.max(0, itemIndex);
}

function resolveEmptyIndex(
  emptyCount: number,
  itemStartIndex: number,
  previousBoundaryIndex: number | null,
): number {
  if (emptyCount === 0) {
    return itemStartIndex;
  }
  return virtualIndexForDataIndex(itemStartIndex, 0, previousBoundaryIndex);
}

export function headerIndex(previousBoundaryIndex: number | null): number {
  return previousBoundaryIndex === 0 ? 1 : 0;
}

export function virtualIndexForDataIndex(
  itemStartIndex: number,
  dataIndex: number,
  previousBoundaryIndex: number | null,
): number {
  const virtualIndex = itemStartIndex + dataIndex;
  return previousBoundaryIndex !== null && virtualIndex >= previousBoundaryIndex
    ? virtualIndex + 1
    : virtualIndex;
}

export function dataIndexForVirtualIndex(
  virtualIndex: number,
  itemStartIndex: number,
  previousBoundaryIndex: number | null,
): number | null {
  if (previousBoundaryIndex !== null && virtualIndex === previousBoundaryIndex) {
    return null;
  }
  const shift = previousBoundaryIndex !== null && virtualIndex > previousBoundaryIndex ? 1 : 0;
  const dataIndex = virtualIndex - itemStartIndex - shift;
  return dataIndex < 0 ? null : dataIndex;
}
