import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type HTMLAttributes,
  type ReactElement,
} from "react";

import {
  VirtualizedInfiniteList,
  type VirtualizedInfiniteListBoundaryState,
  type VirtualizedPixelOffsetRequest,
  useStableCallback,
} from "@/ui";

import type { TranscriptRenderItem, TranscriptRenderSlots } from "./renderSlots";
import type { TranscriptWindowSnapshot } from "./types";

export type TranscriptViewportMeasurement = Readonly<{
  absoluteScrollOffsetPx: number;
  viewportExtentPx: number;
  loadedContentEndExtentPx: number;
  edges: Readonly<{
    olderAvailable: boolean;
    newerAvailable: boolean;
  }>;
  anchor: Readonly<{
    presentationKey: string;
    beforeViewportOffsetPx: number;
    afterViewportOffsetPx: number;
  }> | null;
}>;

export type TranscriptWindowViewProps = Readonly<{
  snapshot: TranscriptWindowSnapshot;
  slots: TranscriptRenderSlots<ReactElement | null>;
  estimateSize: () => number;
  loadingLabel: string;
  onLoadOlder: () => void;
  onLoadNewer: () => void;
  onMeasurement: (measurement: TranscriptViewportMeasurement) => void;
  previousBoundary?: VirtualizedInfiniteListBoundaryState | undefined;
  nextBoundary?: VirtualizedInfiniteListBoundaryState | undefined;
  pixelOffsetRequest?: VirtualizedPixelOffsetRequest | undefined;
}>;

type VisibleAnchor = Readonly<{
  presentationKey: string;
  viewportOffsetPx: number;
}>;

type TranscriptRowWrapperProps = HTMLAttributes<HTMLDivElement> &
  Readonly<{ "data-transcript-presentation-key": string }>;

const presentationKeyAttribute = "data-transcript-presentation-key";

export function TranscriptWindowView({
  snapshot,
  slots,
  estimateSize,
  loadingLabel,
  onLoadOlder,
  onLoadNewer,
  onMeasurement,
  previousBoundary,
  nextBoundary,
  pixelOffsetRequest,
}: TranscriptWindowViewProps) {
  const [scrollElement, setScrollElement] = useState<HTMLDivElement | null>(null);
  const anchorRef = useRef<VisibleAnchor | null>(null);
  const emitMeasurement = useStableCallback(onMeasurement);
  const olderAvailable = snapshot.older.cursor !== null;
  const newerAvailable = snapshot.newer.cursor !== null;
  const measure = useCallback(() => {
    if (scrollElement === null) return;
    const rows = mountedRows(scrollElement);
    const viewport = scrollElement.getBoundingClientRect();
    const previousAnchor = anchorRef.current;
    const survivingRow =
      previousAnchor === null
        ? undefined
        : rows.find((row) => row.dataset.transcriptPresentationKey === previousAnchor.presentationKey);
    emitMeasurement({
      absoluteScrollOffsetPx: scrollElement.scrollTop,
      viewportExtentPx: scrollElement.clientHeight,
      loadedContentEndExtentPx: scrollElement.scrollHeight,
      edges: { olderAvailable, newerAvailable },
      anchor:
        previousAnchor === null || survivingRow === undefined
          ? null
          : {
              presentationKey: previousAnchor.presentationKey,
              beforeViewportOffsetPx: previousAnchor.viewportOffsetPx,
              afterViewportOffsetPx: survivingRow.getBoundingClientRect().top - viewport.top,
            },
    });
    anchorRef.current = firstVisibleAnchor(rows, viewport);
  }, [emitMeasurement, newerAvailable, olderAvailable, scrollElement]);

  useLayoutEffect(() => {
    measure();
  }, [measure, snapshot.items]);

  useEffect(() => {
    if (scrollElement === null) return;
    scrollElement.addEventListener("scroll", measure);
    const observer = new ResizeObserver(measure);
    const observedRows = new Set<HTMLElement>();
    const observeMountedRows = () => {
      const currentRows = new Set(mountedRows(scrollElement));
      for (const row of observedRows) {
        if (!currentRows.has(row)) {
          observer.unobserve(row);
          observedRows.delete(row);
        }
      }
      for (const row of currentRows) {
        if (!observedRows.has(row)) {
          observedRows.add(row);
          observer.observe(row);
        }
      }
    };
    observer.observe(scrollElement);
    observeMountedRows();
    const mountedRowObserver = new MutationObserver(() => {
      observeMountedRows();
      measure();
    });
    mountedRowObserver.observe(scrollElement, { childList: true, subtree: true });
    return () => {
      mountedRowObserver.disconnect();
      observer.disconnect();
      scrollElement.removeEventListener("scroll", measure);
    };
  }, [measure, scrollElement, snapshot.items]);

  return (
    <VirtualizedInfiniteList
      className="h-full min-h-0 w-full min-w-0 overflow-x-hidden overflow-y-auto"
      estimateSize={estimateSize}
      getItemAnchorKey={presentationKey}
      getItemKey={presentationKey}
      getItemWrapperProps={rowWrapperProps}
      hasNextPage={newerAvailable}
      hasPreviousPage={olderAvailable}
      isFetchingNextPage={snapshot.newer.kind === "loading"}
      isFetchingPreviousPage={snapshot.older.kind === "loading"}
      items={snapshot.items}
      layoutChangeScrollBehavior="natural"
      loadingLabel={loadingLabel}
      nextBoundary={nextBoundary}
      onLoadMore={onLoadNewer}
      onLoadPrevious={onLoadOlder}
      onScrollElementChange={setScrollElement}
      pixelOffsetRequest={pixelOffsetRequest}
      previousBoundary={previousBoundary}
      renderItem={(item) => (
        <div className="mx-auto w-full max-w-[1200px]">
          <TranscriptFamilySlot item={item} slots={slots} />
        </div>
      )}
    />
  );
}

function presentationKey(item: TranscriptRenderItem): string {
  return item.key;
}

function rowWrapperProps(item: TranscriptRenderItem): TranscriptRowWrapperProps {
  return { "data-transcript-presentation-key": item.key };
}

function mountedRows(scrollElement: HTMLElement): readonly HTMLElement[] {
  return Array.from(scrollElement.querySelectorAll<HTMLElement>(`[${presentationKeyAttribute}]`));
}

function firstVisibleAnchor(rows: readonly HTMLElement[], viewport: DOMRect): VisibleAnchor | null {
  const row = rows.find((candidate) => {
    const bounds = candidate.getBoundingClientRect();
    return bounds.bottom > viewport.top && bounds.top < viewport.bottom;
  });
  return row === undefined
    ? null
    : {
        presentationKey: requiredPresentationKey(row),
        viewportOffsetPx: row.getBoundingClientRect().top - viewport.top,
      };
}

function requiredPresentationKey(row: HTMLElement): string {
  const key = row.dataset.transcriptPresentationKey;
  if (key === undefined) throw new Error("Mounted transcript row is missing its presentation key.");
  return key;
}

function TranscriptFamilySlot({
  item,
  slots,
}: Readonly<{
  item: TranscriptRenderItem;
  slots: TranscriptRenderSlots<ReactElement | null>;
}>): ReactElement | null {
  switch (item.kind) {
    case "user":
      return slots.user(item);
    case "assistant":
      return slots.assistant(item);
    case "tool":
      return slots.tool(item);
    case "reasoning_trace":
      return slots.reasoning(item);
    case "notice":
    case "reviewer_feedback":
    case "reviewer_error":
      return slots.notice(item);
    case "thinking_status":
      return slots.thinkingStatus(item);
  }
}
