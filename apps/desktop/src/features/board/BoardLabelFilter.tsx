import { useCallback, useLayoutEffect } from "react";
import { useTranslation } from "react-i18next";
import { XIcon } from "lucide-react";

import {
  LabelChooser,
  type LabelMembershipRefreshEffect,
  reduceLabelFilterState,
  useProjectLabelFilter,
} from "@/shared/labels";
import { Button, useStableCallback } from "@/ui";
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
      ? t("labels.filterCount", { count: filter.state.filter.labelIDs.length })
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
    <div className="flex h-9 shrink-0 items-center gap-[var(--space-1)] px-[var(--space-2)]">
      <LabelChooser
        invocation={{
          kind: "filter",
          state: filter.state,
          onAction: dispatch,
        }}
        trigger={
          <Button className="h-7 px-[var(--space-2)]" variant="ghost">
            {summary}
          </Button>
        }
      />
      {active ? (
        <Button
          aria-label={t("labels.clearFilter")}
          className="h-7 w-7"
          onClick={() => {
            dispatch({ type: "clear" });
          }}
          size="icon-sm"
          variant="ghost"
        >
          <XIcon aria-hidden="true" size={15} strokeWidth={1.75} />
        </Button>
      ) : null}
    </div>
  );
}
