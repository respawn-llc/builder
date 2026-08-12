import { useMemo, type ReactNode } from "react";

import { InfiniteListBoundary, type VirtualizedInfiniteListBoundaryState } from "./InfiniteListBoundary";
import { Spinner } from "./Spinner";
import {
  VirtualizedFrame,
  type VirtualizedFrameEntry,
  type VirtualizedFrameLoadTrigger,
} from "./VirtualizedFrame";
import { resolveNextLoadEdge, resolvePreviousLoadEdge } from "./virtualizedInfiniteListLoadMore";
import type { VirtualizedPixelOffsetRequest } from "./virtualizedPixelOffsetRequest";

export type { VirtualizedInfiniteListBoundaryState } from "./InfiniteListBoundary";

const previousBoundaryKey = "boundary-previous";
const headerKey = "header";
const emptyKey = "empty";
const nextBoundaryKey = "boundary-next";

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
  role?: "list" | "listbox" | undefined;
  rowSpacing?: "default" | "compact" | "tight" | undefined;
  testId?: string | undefined;
  initialScrollKey?: string | undefined;
  initialScrollRequestKey?: string | undefined;
  initialScrollAlign?: "auto" | "start" | undefined;
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
  pixelOffsetRequest?: VirtualizedPixelOffsetRequest | undefined;
}>;

export function VirtualizedInfiniteList<TItem>(props: VirtualizedInfiniteListProps<TItem>) {
  return <VirtualizedInfiniteListContent {...resolveVirtualizedInfiniteListProps(props)} />;
}

type VirtualizedInfiniteListResolvedProps<TItem> = Omit<
  VirtualizedInfiniteListProps<TItem>,
  | "getItemAnchorKey"
  | "getItemOccurrenceKey"
  | "role"
  | "rowSpacing"
  | "initialScrollAlign"
  | "paddingEnd"
  | "paddingStart"
  | "hasPreviousPage"
  | "isFetchingPreviousPage"
> & {
  getItemAnchorKey: (item: TItem) => string;
  getItemOccurrenceKey: (item: TItem) => string;
  role: "list" | "listbox";
  rowSpacing: "default" | "compact" | "tight";
  initialScrollAlign: "auto" | "start";
  paddingEnd: number;
  paddingStart: number;
  hasPreviousPage: boolean;
  isFetchingPreviousPage: boolean;
};

function resolveVirtualizedInfiniteListProps<TItem>(
  props: VirtualizedInfiniteListProps<TItem>,
): VirtualizedInfiniteListResolvedProps<TItem> {
  return {
    ...props,
    getItemAnchorKey: props.getItemAnchorKey ?? props.getItemKey,
    getItemOccurrenceKey: props.getItemOccurrenceKey ?? props.getItemKey,
    role: props.role ?? "list",
    rowSpacing: props.rowSpacing ?? "default",
    initialScrollAlign: props.initialScrollAlign ?? "start",
    paddingEnd: props.paddingEnd ?? 0,
    paddingStart: props.paddingStart ?? 0,
    hasPreviousPage: props.hasPreviousPage ?? false,
    isFetchingPreviousPage: props.isFetchingPreviousPage ?? false,
  };
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
  rowSpacing,
  testId,
  initialScrollKey,
  initialScrollRequestKey,
  initialScrollAlign,
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
  pixelOffsetRequest,
}: VirtualizedInfiniteListResolvedProps<TItem>) {
  const entries = useMemo(
    () =>
      singleStreamEntries({
        empty,
        getItemAnchorKey,
        getItemKey,
        getItemOccurrenceKey,
        hasNextPage,
        header,
        isFetchingNextPage,
        items,
        loadingLabel,
        nextBoundary,
        previousBoundary,
        renderItem,
      }),
    [
      empty,
      getItemAnchorKey,
      getItemKey,
      getItemOccurrenceKey,
      hasNextPage,
      header,
      isFetchingNextPage,
      items,
      loadingLabel,
      nextBoundary,
      previousBoundary,
      renderItem,
    ],
  );
  const itemKeys = useMemo(() => items.map(getItemKey), [getItemKey, items]);
  const itemKeySet = useMemo(() => new Set(itemKeys), [itemKeys]);
  const itemStartIndex = Number(previousBoundary !== undefined) + Number(header !== undefined);
  const loadTriggers = useMemo<readonly VirtualizedFrameLoadTrigger[]>(
    () => [
      {
        key: `previous:${previousLoadKey ?? items.length.toString()}`,
        isAtEdge: (visibleKeys) =>
          resolvePreviousLoadEdge({
            getItemKey,
            itemStartIndex,
            items,
            previousLoadItemKey,
            visibleIndexes: visibleKeys.flatMap((key) => {
              const index = entries.findIndex((entry) => entry.key === key);
              return index < 0 ? [] : [index];
            }),
          }),
        hasNextPage: hasPreviousPage,
        isFetchingNextPage: isFetchingPreviousPage,
        onLoadMore: onLoadPrevious,
      },
      {
        key: `next:${loadMoreKey ?? items.length.toString()}`,
        isAtEdge: (visibleKeys) =>
          resolveNextLoadEdge(
            itemStartIndex,
            items.length,
            visibleKeys.flatMap((key) => {
              const index = entries.findIndex((entry) => entry.key === key);
              return index < 0 ? [] : [index];
            }),
          ),
        hasNextPage,
        isFetchingNextPage,
        onLoadMore,
      },
    ],
    [
      hasNextPage,
      hasPreviousPage,
      isFetchingNextPage,
      isFetchingPreviousPage,
      entries,
      getItemKey,
      itemStartIndex,
      items,
      loadMoreKey,
      onLoadMore,
      onLoadPrevious,
      previousLoadItemKey,
      previousLoadKey,
    ],
  );
  const framePinnedKeys = useMemo(() => {
    if (pinnedItemKeys === undefined) return undefined;
    return new Set([...pinnedItemKeys].filter((key) => itemKeySet.has(key)));
  }, [itemKeySet, pinnedItemKeys]);

  return (
    <VirtualizedFrame
      ariaLabel={ariaLabel}
      canApplyPixelOffset={items.length > 0}
      className={className}
      entries={entries}
      estimateSize={estimateSize}
      id={id}
      initialScrollAlign={initialScrollAlign}
      initialScrollKey={initialScrollKey}
      initialScrollRequestKey={initialScrollRequestKey}
      loadTriggers={loadTriggers}
      nonAdjustingResizeItemKey={nonAdjustingResizeItemKey}
      onScrollElementChange={onScrollElementChange}
      paddingEnd={paddingEnd}
      paddingStart={paddingStart}
      pinnedItemKeys={framePinnedKeys}
      pixelOffsetRequest={pixelOffsetRequest}
      role={role}
      rowRole={role === "listbox" ? "presentation" : "listitem"}
      rowSpacing={rowSpacing}
      testId={testId}
    />
  );
}

function singleStreamEntries<TItem>({
  empty,
  getItemAnchorKey,
  getItemKey,
  getItemOccurrenceKey,
  hasNextPage,
  header,
  isFetchingNextPage,
  items,
  loadingLabel,
  nextBoundary,
  previousBoundary,
  renderItem,
}: Readonly<{
  empty: ReactNode | undefined;
  getItemAnchorKey: (item: TItem) => string;
  getItemKey: (item: TItem) => string;
  getItemOccurrenceKey: (item: TItem) => string;
  hasNextPage: boolean;
  header: ReactNode | undefined;
  isFetchingNextPage: boolean;
  items: readonly TItem[];
  loadingLabel: string;
  nextBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  previousBoundary: VirtualizedInfiniteListBoundaryState | undefined;
  renderItem: (item: TItem, itemIndex: number) => ReactNode;
}>): readonly VirtualizedFrameEntry[] {
  const entries: VirtualizedFrameEntry[] = [];
  if (previousBoundary !== undefined) {
    entries.push({
      key: previousBoundaryKey,
      render: (): ReactNode => <InfiniteListBoundary direction="previous" state={previousBoundary} />,
    });
  }
  if (header !== undefined) {
    entries.push({ key: headerKey, render: (): ReactNode => header });
  }
  if (items.length === 0 && empty !== undefined) {
    entries.push({ key: emptyKey, render: (): ReactNode => empty });
  } else {
    items.forEach((item, itemIndex) => {
      const key = getItemKey(item);
      entries.push({
        key,
        anchorKey: getItemAnchorKey(item),
        occurrenceKey: getItemOccurrenceKey(item),
        render: (): ReactNode => renderItem(item, itemIndex),
      });
    });
  }
  if (nextBoundary !== undefined) {
    entries.push({
      key: nextBoundaryKey,
      render: (): ReactNode => <InfiniteListBoundary direction="next" state={nextBoundary} />,
    });
  } else if (hasNextPage) {
    entries.push({
      key: `placeholder-${entries.length.toString()}`,
      render: (): ReactNode => (
        <VirtualizedPlaceholder loading={isFetchingNextPage} loadingLabel={loadingLabel} />
      ),
    });
  }
  return entries;
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
