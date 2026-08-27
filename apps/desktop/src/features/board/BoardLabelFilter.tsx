import { useTranslation } from "react-i18next";
import { CheckCircle2Icon, FilterIcon, XIcon } from "lucide-react";

import { taskLabelFilterConditionCount } from "@/api";
import { LabelChooser, useProjectLabelFilter } from "@/shared/labels";
import { AnimatedChipSummary, Button, InteractiveChip, cx } from "@/ui";
import { useBoardQuery } from "./BoardQueryRuntime";
import { BoardSortChrome } from "./BoardSortChrome";

export function BoardFilterChrome() {
  const { t } = useTranslation();
  const filter = useProjectLabelFilter();
  const boardQuery = useBoardQuery();
  const active = filter.state.filter.kind !== "none";
  const summary =
    filter.state.filter.kind === "named"
      ? t("labels.filterCount", { count: taskLabelFilterConditionCount(filter.state.filter) })
      : filter.state.filter.kind === "unlabeled"
        ? t("labels.unlabeled")
        : t("labels.filter");
  const unblocked = boardQuery.filter.dependencyFilter === true;
  return (
    <>
      <span className="relative inline-flex">
        <LabelChooser
          invocation={{
            kind: "filter",
            state: filter.state,
            onAction: filter.dispatch,
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
              <AnimatedChipSummary text={summary} />
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
              filter.dispatch({ type: "clear" });
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
        onClick={() => {
          boardQuery.setDependencyFilter(unblocked ? null : true);
        }}
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
