import {
  useCallback,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type MouseEvent,
  type ReactNode,
} from "react";
import { ArrowDown, ArrowUp, ChevronRight } from "lucide-react";

import { cx } from "./classes";
import { IconTooltipButton } from "./IconTooltipButton";
import { InfiniteListBoundary, type VirtualizedInfiniteListBoundaryState } from "./InfiniteListBoundary";
import { Spinner } from "./Spinner";
import {
  VirtualizedFrame,
  type VirtualizedFrameEntry,
  type VirtualizedFrameLoadTrigger,
  type VirtualizedFrameScrollCommand,
  type VirtualizedFrameScrollMetrics,
} from "./VirtualizedFrame";
import type { VirtualizedPixelOffsetRequest } from "./virtualizedPixelOffsetRequest";

export type VirtualizedGroupedGridCell = Readonly<{
  key: string;
  content: ReactNode;
  ariaLabel?: string | undefined;
  className?: string | undefined;
}>;

export type VirtualizedGroupedGridEntry =
  | Readonly<{
      kind: "column-header";
      key: string;
      cells: readonly VirtualizedGroupedGridCell[];
      className?: string | undefined;
    }>
  | Readonly<{
      kind: "group-header";
      key: string;
      groupKey: string;
      label: string;
      count: number;
      ariaLabel: string;
      expanded: boolean;
      onToggle: () => void;
      className?: string | undefined;
    }>
  | Readonly<{
      kind: "boundary";
      key: string;
      groupKey: string;
      direction: "initial" | "previous" | "next" | "replacement";
      state?: VirtualizedInfiniteListBoundaryState | undefined;
      hasMore?: boolean | undefined;
      isFetching?: boolean | undefined;
      loadingLabel: string;
      loadKey?: string | undefined;
      onLoadMore?: (() => void) | undefined;
      className?: string | undefined;
    }>
  | Readonly<{
      kind: "task";
      key: string;
      groupKey: string;
      ariaLabel: string;
      cells: readonly VirtualizedGroupedGridCell[];
      anchorKey?: string | undefined;
      occurrenceKey?: string | undefined;
      selected?: boolean | undefined;
      onActivate?: ((event: MouseEvent<HTMLDivElement>) => void) | undefined;
      onKeyDown?: ((event: KeyboardEvent<HTMLDivElement>) => void) | undefined;
      className?: string | undefined;
    }>;

export type VirtualizedGroupedGridProps = Readonly<{
  entries: readonly VirtualizedGroupedGridEntry[];
  columnCount: number;
  estimateSize: () => number;
  ariaLabel: string;
  canApplyPixelOffset?: boolean | undefined;
  id?: string | undefined;
  testId?: string | undefined;
  className?: string | undefined;
  rowSpacing?: "default" | "compact" | "tight" | undefined;
  paddingEnd?: number | undefined;
  paddingStart?: number | undefined;
  onScrollElementChange?: ((element: HTMLDivElement | null) => void) | undefined;
  pixelOffsetRequest?: VirtualizedPixelOffsetRequest | undefined;
  onScrollRequestApplied?: ((key: string) => void) | undefined;
  scrollRequest?: VirtualizedFrameScrollCommand | undefined;
  navigation?:
    | Readonly<{
        upLabel: string;
        downLabel: string;
        finalEntryKey: string;
        onRequestFinalEntry?: (() => void) | undefined;
        onRequestTop?: (() => void) | undefined;
        requestKey?: string | null | undefined;
        requiresFinalEntryRequest?: boolean | undefined;
        upDisabled?: boolean | undefined;
        downDisabled?: boolean | undefined;
      }>
    | undefined;
}>;

export function VirtualizedGroupedGrid({
  entries,
  columnCount,
  estimateSize,
  ariaLabel,
  canApplyPixelOffset = entries.some((entry) => entry.kind === "task"),
  id,
  testId,
  className,
  rowSpacing = "tight",
  paddingEnd = 0,
  paddingStart = 0,
  onScrollElementChange,
  pixelOffsetRequest,
  onScrollRequestApplied,
  scrollRequest,
  navigation,
}: VirtualizedGroupedGridProps) {
  requirePositiveColumnCount(columnCount);
  const navigationState = useGroupedGridNavigation(entries, navigation);
  const [cancelledRequestKey, setCancelledRequestKey] = useState<string | null>(null);
  const scrollCommand = preferredGroupedGridScrollCommand(
    scrollRequest,
    cancelledRequestKey,
    navigationState.scrollCommand,
  );
  const requestTop = () => {
    setCancelledRequestKey(navigation?.requestKey ?? null);
    navigationState.requestTop();
  };
  const frameEntries = useMemo(
    () => entries.map((entry) => groupedFrameEntry(entry, columnCount)),
    [columnCount, entries],
  );
  const loadTriggers = useMemo(() => groupedLoadTriggers(entries), [entries]);
  const pinnedItemKeys = useMemo(
    () => new Set(entries.flatMap((entry) => (entry.kind === "column-header" ? [entry.key] : []))),
    [entries],
  );
  const [scrollMetrics, setScrollMetrics] = useState<VirtualizedFrameScrollMetrics>({
    atBottom: true,
    atTop: true,
    overflows: false,
  });
  const handleScrollMetricsChange = useCallback((next: VirtualizedFrameScrollMetrics) => {
    setScrollMetrics((current) =>
      current.atBottom === next.atBottom &&
      current.atTop === next.atTop &&
      current.overflows === next.overflows
        ? current
        : next,
    );
  }, []);
  return (
    <div className="relative">
      <VirtualizedFrame
        ariaLabel={ariaLabel}
        canApplyPixelOffset={canApplyPixelOffset}
        className={className}
        entries={frameEntries}
        estimateSize={estimateSize}
        id={id}
        initialScrollAlign="start"
        loadTriggers={loadTriggers}
        onScrollElementChange={onScrollElementChange}
        onScrollCommandApplied={onScrollRequestApplied}
        onScrollMetricsChange={handleScrollMetricsChange}
        paddingEnd={paddingEnd}
        paddingStart={paddingStart}
        pinnedItemKeys={pinnedItemKeys}
        pixelOffsetRequest={pixelOffsetRequest}
        role="grid"
        rowRole="row"
        rowSpacing={rowSpacing}
        scrollCommand={scrollCommand}
        testId={testId}
      />
      {navigation === undefined || !scrollMetrics.overflows ? null : (
        <div className="absolute right-[var(--space-3)] bottom-[var(--space-3)] z-[2] flex flex-col">
          <IconTooltipButton
            disabled={navigation.upDisabled === true || scrollMetrics.atTop}
            label={navigation.upLabel}
            onClick={requestTop}
            size="icon-sm"
          >
            <ArrowUp aria-hidden="true" size={16} />
          </IconTooltipButton>
          <IconTooltipButton
            disabled={
              navigation.downDisabled === true ||
              scrollMetrics.atBottom ||
              (!navigationState.finalEntryReady &&
                navigation.requiresFinalEntryRequest !== true)
            }
            label={navigation.downLabel}
            onClick={navigationState.requestFinalEntry}
            size="icon-sm"
          >
            <ArrowDown aria-hidden="true" size={16} />
          </IconTooltipButton>
        </div>
      )}
    </div>
  );
}

function preferredGroupedGridScrollCommand(
  requested: VirtualizedFrameScrollCommand | undefined,
  cancelledRequestKey: string | null,
  navigation: VirtualizedFrameScrollCommand | undefined,
): VirtualizedFrameScrollCommand | undefined {
  if (requested?.key === cancelledRequestKey) return navigation;
  return requested ?? navigation;
}

function requirePositiveColumnCount(columnCount: number): void {
  if (Number.isInteger(columnCount) && columnCount >= 1) return;
  throw new Error(
    `virtualized grouped grid column count must be a positive integer: ${columnCount.toString()}`,
  );
}

function useGroupedGridNavigation(
  entries: readonly VirtualizedGroupedGridEntry[],
  navigation: VirtualizedGroupedGridProps["navigation"],
) {
  const finalEntryReady =
    navigation !== undefined && entries.some((entry) => entry.key === navigation.finalEntryKey);
  const [scrollCommand, setScrollCommand] = useState<
    | Readonly<{ key: string; target: "top" }>
    | Readonly<{ align: "end"; entryKey: string; key: string; target: "entry" }>
    | undefined
  >();
  const sequenceRef = useRef(0);
  const nextSequence = () => {
    sequenceRef.current += 1;
    return sequenceRef.current;
  };
  const requestTop = () => {
    navigation?.onRequestTop?.();
    setScrollCommand({ key: `top-${nextSequence().toString()}`, target: "top" });
  };
  const requestFinalEntry = () => {
    const request = requestedFinalEntryAction({
      finalEntryReady,
      navigation,
      sequence: nextSequence,
    });
    if (request?.kind === "load") request.run();
    if (request?.kind === "scroll") setScrollCommand(request.command);
  };
  const requestedScrollCommand =
    scrollCommand !== undefined ||
    navigation?.requestKey === undefined ||
    navigation.requestKey === null ||
    !finalEntryReady
      ? scrollCommand
      : {
          align: "end" as const,
          entryKey: navigation.finalEntryKey,
          key: navigation.requestKey,
          target: "entry" as const,
        };
  return {
    finalEntryReady,
    requestFinalEntry,
    requestTop,
    scrollCommand: requestedScrollCommand,
  };
}

function requestedFinalEntryAction({
  finalEntryReady,
  navigation,
  sequence,
}: Readonly<{
  finalEntryReady: boolean;
  navigation: VirtualizedGroupedGridProps["navigation"];
  sequence: () => number;
}>):
  | Readonly<{ kind: "load"; run: () => void }>
  | Readonly<{
      command: Readonly<{ align: "end"; entryKey: string; key: string; target: "entry" }>;
      kind: "scroll";
    }>
  | null {
  if (navigation === undefined) return null;
  if (
    navigation.requiresFinalEntryRequest === true &&
    navigation.onRequestFinalEntry !== undefined
  ) {
    return { kind: "load", run: navigation.onRequestFinalEntry };
  }
  if (!finalEntryReady) return null;
  return {
    command: {
      align: "end",
      entryKey: navigation.finalEntryKey,
      key: `final-${sequence().toString()}`,
      target: "entry",
    },
    kind: "scroll",
  };
}

function groupedFrameEntry(entry: VirtualizedGroupedGridEntry, columnCount: number): VirtualizedFrameEntry {
  switch (entry.kind) {
    case "column-header":
      return {
        key: entry.key,
        kind: "column-header",
        sticky: true,
        className: entry.className,
        render: () => (
          <>
            {entry.cells.map((cell) => (
              <div aria-label={cell.ariaLabel} className={cell.className} key={cell.key} role="columnheader">
                {cell.content}
              </div>
            ))}
          </>
        ),
      };
    case "group-header":
      return {
        key: entry.key,
        kind: "group-header",
        className: entry.className,
        render: () => (
          <div aria-colspan={columnCount} role="gridcell">
            <button aria-expanded={entry.expanded} className="w-full" onClick={entry.onToggle} type="button">
              <ChevronRight
                aria-hidden="true"
                className={cx(
                  "inline-block transition-transform motion-reduce:transition-none",
                  entry.expanded && "rotate-90",
                )}
                size={16}
              />
              <span aria-hidden="true">
                {entry.label} {entry.count}
              </span>
              <span className="sr-only">{entry.ariaLabel}</span>
            </button>
          </div>
        ),
      };
    case "boundary":
      return {
        key: entry.key,
        kind: "boundary",
        className: entry.className,
        render: () => (
          <div aria-colspan={columnCount} role="gridcell">
            {entry.state === undefined ? (
              <div
                aria-label={entry.isFetching === true ? entry.loadingLabel : undefined}
                aria-live="polite"
                className="grid min-h-12 place-items-center"
                role={entry.isFetching === true ? "status" : undefined}
              >
                {entry.isFetching === true ? <Spinner size="sm" /> : null}
              </div>
            ) : (
              <InfiniteListBoundary direction={entry.direction} state={entry.state} />
            )}
          </div>
        ),
      };
    case "task":
      return {
        key: entry.key,
        kind: "content",
        anchorKey: entry.anchorKey ?? entry.key,
        occurrenceKey: entry.occurrenceKey ?? entry.key,
        className: entry.className,
        ariaLabel: entry.ariaLabel,
        ariaSelected: entry.selected,
        onClick: entry.onActivate,
        onKeyDown: entry.onKeyDown,
        tabIndex: 0,
        render: () => (
          <>
            {entry.cells.map((cell) => (
              <div aria-label={cell.ariaLabel} className={cell.className} key={cell.key} role="gridcell">
                {cell.content}
              </div>
            ))}
          </>
        ),
      };
  }
}

function groupedLoadTriggers(
  entries: readonly VirtualizedGroupedGridEntry[],
): readonly VirtualizedFrameLoadTrigger[] {
  return entries.flatMap((entry) => {
    if (
      entry.kind !== "boundary" ||
      entry.direction === "initial" ||
      entry.direction === "replacement"
    ) {
      return [];
    }
    return [
      {
        key: `${entry.groupKey}:${entry.direction}:${entry.loadKey ?? entry.key}`,
        isAtEdge: (visibleEntryKeys: readonly string[]) => visibleEntryKeys.includes(entry.key),
        hasNextPage: entry.hasMore ?? false,
        isFetchingNextPage: entry.isFetching ?? false,
        onLoadMore: entry.onLoadMore,
      },
    ];
  });
}
