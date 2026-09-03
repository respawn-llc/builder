import type { VirtualItem, ReactVirtualizer } from "@tanstack/react-virtual";
import { useCallback, useLayoutEffect, useRef } from "react";

export type VirtualizedLeadingAnchor = Readonly<{
  anchorKey: string;
  occurrenceKey: string;
  inRowOffset: number;
}>;

export type VirtualizedAnchorEntry = Readonly<{
  anchorKey: string;
  occurrenceKey: string;
}>;

export function recoverableLeadingAnchor(
  anchor: VirtualizedLeadingAnchor | null,
  appliedPixelOffsetKey: string | null,
  requestKey: string | undefined,
): VirtualizedLeadingAnchor | null {
  return appliedPixelOffsetKey === requestKey ? null : anchor;
}

function findAnchorEntryIndex(
  entries: readonly VirtualizedAnchorEntry[],
  anchor: VirtualizedLeadingAnchor,
  includeOccurrence: boolean,
): number {
  return entries.findIndex(
    (entry) =>
      entry.anchorKey === anchor.anchorKey &&
      (!includeOccurrence || entry.occurrenceKey === anchor.occurrenceKey),
  );
}

export function resolveAnchorEntryIndex(
  entries: readonly VirtualizedAnchorEntry[],
  anchor: VirtualizedLeadingAnchor,
): number {
  const occurrenceIndex = findAnchorEntryIndex(entries, anchor, true);
  return occurrenceIndex >= 0 ? occurrenceIndex : findAnchorEntryIndex(entries, anchor, false);
}

export function useVirtualizedLeadingAnchor<TItem>({
  behavior,
  estimateSize,
  getItemAnchorKey,
  getItemOccurrenceKey,
  isFallbackRendering,
  itemStartIndex,
  items,
  orientation,
  paddingStart,
  pixelOffsetAppliedKeyRef,
  pixelOffsetRequestKey,
  scrollRef,
  virtualItems,
  virtualizer,
}: Readonly<{
  behavior: "preserve-leading-item" | "natural";
  estimateSize: () => number;
  getItemAnchorKey: (item: TItem) => string;
  getItemOccurrenceKey: (item: TItem) => string;
  isFallbackRendering: boolean;
  itemStartIndex: number;
  items: readonly TItem[];
  orientation: "vertical" | "horizontal";
  paddingStart: number;
  pixelOffsetAppliedKeyRef: { current: string | null };
  pixelOffsetRequestKey: string | undefined;
  scrollRef: { current: HTMLDivElement | null };
  virtualItems: readonly VirtualItem[];
  virtualizer: ReactVirtualizer<HTMLDivElement, Element>;
}>): (() => void) | undefined {
  const leadingAnchorRef = useRef<VirtualizedLeadingAnchor | null>(null);
  const previousAnchorEntriesRef = useRef<readonly VirtualizedAnchorEntry[]>([]);
  const captureLeadingAnchor = useCallback(() => {
    const element = scrollRef.current;
    if (element === null || items.length === 0) {
      leadingAnchorRef.current = null;
      return;
    }
    const scrollOffset = orientation === "horizontal" ? element.scrollLeft : element.scrollTop;
    const virtualItem = virtualizer.getVirtualItems().find((item) => {
      if (item.index < itemStartIndex || item.index >= itemStartIndex + items.length) {
        return false;
      }
      if (orientation === "horizontal") {
        return item.start < scrollOffset + element.clientWidth && item.end > scrollOffset;
      }
      return item.start <= scrollOffset && item.end > scrollOffset;
    });
    if (virtualItem !== undefined) {
      const item = items[virtualItem.index - itemStartIndex];
      leadingAnchorRef.current =
        item === undefined
          ? null
          : {
              anchorKey: getItemAnchorKey(item),
              occurrenceKey: getItemOccurrenceKey(item),
              inRowOffset:
                orientation === "horizontal"
                  ? virtualItem.start - scrollOffset
                  : scrollOffset - virtualItem.start,
            };
      return;
    }
    if (orientation === "horizontal") {
      leadingAnchorRef.current = null;
      return;
    }
    const estimatedSize = Math.max(1, estimateSize());
    const estimatedVirtualIndex = Math.max(
      itemStartIndex,
      Math.floor(Math.max(0, scrollOffset - paddingStart) / estimatedSize),
    );
    const dataIndex = Math.min(items.length - 1, estimatedVirtualIndex - itemStartIndex);
    const item = items[dataIndex];
    leadingAnchorRef.current =
      item === undefined
        ? null
        : {
            anchorKey: getItemAnchorKey(item),
            occurrenceKey: getItemOccurrenceKey(item),
            inRowOffset: scrollOffset - (paddingStart + estimatedVirtualIndex * estimatedSize),
          };
  }, [
    estimateSize,
    getItemAnchorKey,
    getItemOccurrenceKey,
    itemStartIndex,
    items,
    orientation,
    paddingStart,
    scrollRef,
    virtualizer,
  ]);

  useLayoutEffect(() => {
    if (behavior === "natural") {
      leadingAnchorRef.current = null;
      previousAnchorEntriesRef.current = [];
      return;
    }
    const currentEntries = items.map((item) => ({
      anchorKey: getItemAnchorKey(item),
      occurrenceKey: getItemOccurrenceKey(item),
    }));
    const previousEntries = previousAnchorEntriesRef.current;
    const anchor = recoverableLeadingAnchor(
      leadingAnchorRef.current,
      pixelOffsetAppliedKeyRef.current,
      pixelOffsetRequestKey,
    );
    if (previousEntries.length > 0 && anchor !== null && scrollRef.current !== null) {
      restoreLeadingAnchor({
        anchor,
        currentEntries,
        element: scrollRef.current,
        estimateSize,
        isFallbackRendering,
        itemStartIndex,
        orientation,
        paddingStart,
        previousEntries,
        virtualizer,
      });
    }
    previousAnchorEntriesRef.current = currentEntries;
    pixelOffsetAppliedKeyRef.current = null;
    captureLeadingAnchor();
  }, [
    behavior,
    captureLeadingAnchor,
    estimateSize,
    getItemAnchorKey,
    getItemOccurrenceKey,
    isFallbackRendering,
    itemStartIndex,
    items,
    orientation,
    paddingStart,
    pixelOffsetAppliedKeyRef,
    pixelOffsetRequestKey,
    scrollRef,
    virtualItems,
    virtualizer,
  ]);

  return behavior === "preserve-leading-item" ? captureLeadingAnchor : undefined;
}

function restoreLeadingAnchor({
  anchor,
  currentEntries,
  element,
  estimateSize,
  isFallbackRendering,
  itemStartIndex,
  orientation,
  paddingStart,
  previousEntries,
  virtualizer,
}: Readonly<{
  anchor: VirtualizedLeadingAnchor;
  currentEntries: readonly VirtualizedAnchorEntry[];
  element: HTMLDivElement;
  estimateSize: () => number;
  isFallbackRendering: boolean;
  itemStartIndex: number;
  orientation: "vertical" | "horizontal";
  paddingStart: number;
  previousEntries: readonly VirtualizedAnchorEntry[];
  virtualizer: ReactVirtualizer<HTMLDivElement, Element>;
}>): void {
  const previousIndex = resolveAnchorEntryIndex(previousEntries, anchor);
  const currentIndex = resolveAnchorEntryIndex(currentEntries, anchor);
  if (previousIndex < 0 || currentIndex < 0) {
    return;
  }
  const virtualIndex = itemStartIndex + currentIndex;
  if (orientation === "horizontal") {
    const measuredItem = virtualizer.getVirtualItems().find((item) => item.index === virtualIndex);
    if (measuredItem === undefined) {
      return;
    }
    const scrollOffset = measuredItem.start - anchor.inRowOffset;
    if (element.scrollLeft === scrollOffset) {
      return;
    }
    element.scrollLeft = scrollOffset;
    virtualizer.scrollToOffset(scrollOffset, { behavior: "auto" });
    return;
  }
  const measuredOffset = isFallbackRendering
    ? undefined
    : virtualizer.getOffsetForIndex(virtualIndex, "start")?.[0];
  const rowOffset = measuredOffset ?? paddingStart + virtualIndex * Math.max(1, estimateSize());
  const scrollOffset = rowOffset + anchor.inRowOffset;
  if (element.scrollTop === scrollOffset) {
    return;
  }
  element.scrollTop = scrollOffset;
  if (!isFallbackRendering) {
    virtualizer.scrollToOffset(scrollOffset, { behavior: "auto" });
  }
}
