import { useCallback, useDeferredValue, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { FilterIcon, XIcon } from "lucide-react";

import { taskLabelFilterConditionCount } from "@/api";
import {
  LabelChooser,
  type LabelMembershipRefreshEffect,
  reduceLabelFilterState,
  useProjectLabelFilter,
} from "@/shared/labels";
import { Button, InteractiveChip, cx, useStableCallback } from "@/ui";
import { useBoardFilterGeneration } from "./BoardFilterGenerationRuntime";
import { ignoreBoardMembershipRefresh, type BoardMembershipRefreshRef } from "./BoardMembershipRefresh";

export function BoardMembershipRefreshBinding({
  membershipRefreshRef,
}: Readonly<{
  membershipRefreshRef: BoardMembershipRefreshRef;
}>) {
  const filter = useProjectLabelFilter();
  const generation = useBoardFilterGeneration();
  const refresh = useStableCallback(async (effect: LabelMembershipRefreshEffect) => {
    if (effect.kind === "catalog.deleted") {
      const next = reduceLabelFilterState(filter.state, {
        type: "label.deleted",
        labelID: effect.labelID,
      });
      generation.controller.setDesiredFilter(next.filter);
    }
    const activeGeneration = generation.controller.getSnapshot().active.generation;
    await generation.queryRegistry.invalidateGeneration(activeGeneration);
  });
  useLayoutEffect(() => {
    membershipRefreshRef.current = refresh;
    return () => {
      if (membershipRefreshRef.current === refresh) {
        membershipRefreshRef.current = ignoreBoardMembershipRefresh;
      }
    };
  }, [membershipRefreshRef, refresh]);
  return null;
}

export function BoardLabelFilterChrome() {
  const { t } = useTranslation();
  const filter = useProjectLabelFilter();
  const generation = useBoardFilterGeneration();
  const active = filter.state.filter.kind !== "none";
  const summary =
    filter.state.filter.kind === "named"
      ? t("labels.filterCount", { count: taskLabelFilterConditionCount(filter.state.filter) })
      : filter.state.filter.kind === "unlabeled"
        ? t("labels.unlabeled")
        : t("labels.filter");
  const dispatch = useCallback(
    (action: Parameters<typeof filter.dispatch>[0]): void => {
      const next = reduceLabelFilterState(filter.state, action);
      generation.controller.setDesiredFilter(next.filter);
      filter.dispatch(action);
    },
    [filter, generation.controller],
  );
  return (
    <div className="flex shrink-0 items-center px-[var(--space-2)] pt-[var(--space-2)]">
      <span className="relative inline-flex">
        <LabelChooser
          invocation={{
            kind: "filter",
            state: filter.state,
            onAction: dispatch,
          }}
          trigger={
            <InteractiveChip
              className="board-label-filter-trigger"
              selected={active}
              style={{
                paddingInlineEnd: active ? "var(--space-6)" : "var(--space-3)",
                paddingInlineStart: "var(--space-3)",
              }}
              tone={active ? "primary" : "neutral"}
            >
              <FilterIcon aria-hidden="true" className="shrink-0" size={14} strokeWidth={1.8} />
              <AnimatedFilterSummary text={summary} />
            </InteractiveChip>
          }
        />
        <span
          aria-hidden={active ? undefined : true}
          className={cx(
            "board-label-filter-clear absolute inset-y-0 right-0 z-10 grid overflow-hidden",
            active ? "w-7 scale-100 opacity-100" : "w-0 scale-90 opacity-0",
          )}
          inert={active ? undefined : true}
        >
          <Button
            aria-label={t("labels.clearFilter")}
            className="h-full w-7"
            onClick={() => {
              dispatch({ type: "clear" });
            }}
            size="icon-sm"
            style={{ color: "var(--color-primary)" }}
            variant="ghost"
          >
            <XIcon aria-hidden="true" size={15} strokeWidth={1.75} />
          </Button>
        </span>
      </span>
    </div>
  );
}

function AnimatedFilterSummary({ text }: Readonly<{ text: string }>) {
  const measurementRef = useRef<HTMLSpanElement | null>(null);
  const [width, setWidth] = useState<number | null>(null);
  const deferredText = useDeferredValue(text);
  const outgoingText = deferredText === text ? null : deferredText;
  useLayoutEffect(() => {
    const measurement = measurementRef.current;
    if (measurement === null) {
      return;
    }
    const nextWidth = Math.ceil(measurement.getBoundingClientRect().width);
    setWidth((current) => (current === nextWidth ? current : nextWidth));
  }, [text]);
  return (
    <span
      className="board-label-filter-summary relative inline-block overflow-hidden align-middle"
      style={width === null ? undefined : { width }}
    >
      <span
        aria-hidden="true"
        className="pointer-events-none invisible absolute top-0 left-0 inline-block w-max whitespace-nowrap"
        ref={measurementRef}
      >
        {text}
      </span>
      {outgoingText === null ? null : (
        <span
          aria-hidden="true"
          className="board-label-filter-summary-outgoing pointer-events-none absolute top-0 left-0 inline-block w-max whitespace-nowrap"
          key={`outgoing-${outgoingText}`}
        >
          {outgoingText}
        </span>
      )}
      <span
        className={cx(
          "inline-block w-max whitespace-nowrap",
          outgoingText !== null && "board-label-filter-summary-incoming",
        )}
        key={`incoming-${text}`}
      >
        {text}
      </span>
    </span>
  );
}
