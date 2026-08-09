import type { ReactNode } from "react";
import type { VirtualizedInfiniteListBoundaryState } from "./InfiniteListBoundary";

const boundaryCount = (boundary: unknown): number => Number(boundary !== undefined);
const previousIndex = <T,>(boundary: VirtualizedInfiniteListBoundaryState | undefined, key: string | undefined, items: readonly T[], getKey: (item: T) => string): number | null => boundary === undefined ? null : key === undefined ? 0 : Math.max(0, items.findIndex((item) => getKey(item) === key));

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
}>) {
  const itemStartIndex = Number(hasHeader);
  const emptyCount = Number(itemCount === 0 && empty !== undefined);
  const previousItemIndex = previousIndex(
    previousBoundary,
    previousLoadItemKey,
    items,
    getItemKey,
  );
  const previousBoundaryIndex =
    previousItemIndex === null ? null : itemStartIndex + previousItemIndex;
  const contentCount = Math.max(itemCount, emptyCount);
  const emptyIndex =
    emptyCount === 0 ? itemStartIndex : virtualIndexForDataIndex(itemStartIndex, 0, previousBoundaryIndex);
  const nextContentIndex = itemStartIndex + contentCount + boundaryCount(previousBoundary);
  return {
    count: nextContentIndex + boundaryCount(nextBoundary) +
      Number(nextBoundary === undefined && hasNextPage),
    emptyIndex,
    emptyCount,
    itemStartIndex,
    legacyPlaceholderIndex: nextBoundary === undefined && hasNextPage ? nextContentIndex : null,
    nextBoundaryIndex: nextBoundary === undefined ? null : nextContentIndex,
    previousBoundaryIndex,
  };
}

export const headerIndex = (previousBoundaryIndex: number | null): number => previousBoundaryIndex === 0 ? 1 : 0;

export function virtualIndexForDataIndex(
  itemStartIndex: number,
  dataIndex: number,
  previousBoundaryIndex: number | null,
): number {
  const virtualIndex = itemStartIndex + dataIndex;
  return previousBoundaryIndex !== null && virtualIndex >= previousBoundaryIndex ? virtualIndex + 1 : virtualIndex;
}

export function dataIndexForVirtualIndex(
  virtualIndex: number,
  itemStartIndex: number,
  previousBoundaryIndex: number | null,
): number | null {
  if (previousBoundaryIndex !== null && virtualIndex === previousBoundaryIndex) return null;
  const shift = previousBoundaryIndex !== null && virtualIndex > previousBoundaryIndex ? 1 : 0;
  const dataIndex = virtualIndex - itemStartIndex - shift;
  return dataIndex < 0 ? null : dataIndex;
}
