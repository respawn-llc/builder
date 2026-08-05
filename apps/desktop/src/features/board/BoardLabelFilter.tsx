import { useLayoutEffect } from "react";
import { useTranslation } from "react-i18next";
import { CheckCircle2Icon, FilterIcon, XIcon } from "lucide-react";

import {
  boardFilterWithDependencyFilter,
  boardFilterWithLabelFilter,
  taskLabelFilterConditionCount,
} from "@/api";
import {
  LabelChooser,
  type LabelMembershipRefreshEffect,
  reduceLabelFilterState,
  useProjectLabelFilter,
} from "@/shared/labels";
import { Button, InteractiveChip, cx, useStableCallback } from "@/ui";
import { AnimatedBoardChipSummary } from "./BoardChipSummary";
import { useBoardFilterGeneration } from "./BoardFilterGenerationRuntime";
import { ignoreBoardMembershipRefresh, type BoardMembershipRefreshRef } from "./BoardMembershipRefresh";
import { BoardSortChrome } from "./BoardSortChrome";

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
      const latestSnapshot = generation.controller.getSnapshot();
      const current = latestSnapshot.desiredFilter ?? latestSnapshot.active.filter;
      generation.controller.setDesiredFilter(boardFilterWithLabelFilter(current, next.filter));
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

export function BoardFilterChrome() {
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
  const dispatch = useStableCallback((action: Parameters<typeof filter.dispatch>[0]): void => {
    const next = reduceLabelFilterState(filter.state, action);
    const currentSnapshot = generation.controller.getSnapshot();
    const current = currentSnapshot.desiredFilter ?? currentSnapshot.active.filter;
    generation.controller.setDesiredFilter(boardFilterWithLabelFilter(current, next.filter));
    filter.dispatch(action);
  });
  const current = generation.snapshot.desiredFilter ?? generation.snapshot.active.filter;
  const unblocked = current.dependencyFilter === true;
  const toggleDependencyFilter = useStableCallback(() => {
    const latestSnapshot = generation.controller.getSnapshot();
    const latest = latestSnapshot.desiredFilter ?? latestSnapshot.active.filter;
    generation.controller.setDesiredFilter(
      boardFilterWithDependencyFilter(latest, latest.dependencyFilter === true ? null : true),
    );
  });
  return (
    <>
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
              data-testid="board-label-filter-trigger"
              selected={active}
              style={{
                paddingInlineEnd: active ? "var(--space-6)" : "var(--space-3)",
                paddingInlineStart: "var(--space-3)",
              }}
              tone={active ? "primary" : "neutral"}
            >
              <FilterIcon aria-hidden="true" className="shrink-0" size={14} strokeWidth={1.8} />
              <AnimatedBoardChipSummary text={summary} />
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
      <BoardSortChrome />
      <InteractiveChip
        aria-pressed={unblocked}
        data-testid="board-dependency-filter-trigger"
        onClick={toggleDependencyFilter}
        selected={unblocked}
        style={{ paddingInline: "var(--space-3)" }}
        tone={unblocked ? "primary" : "neutral"}
      >
        <CheckCircle2Icon aria-hidden="true" className="shrink-0" size={14} strokeWidth={1.8} />
        {t("board.unblocked")}
      </InteractiveChip>
    </>
  );
}
