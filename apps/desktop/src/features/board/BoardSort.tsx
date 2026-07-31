import { ArrowUpDown } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  defaultBoardNodeCardsSort,
  type BoardNodeCardsSort,
  type BoardNodeCardsSortField,
} from "@/api";
import {
  InteractiveChip,
  Popover,
  PopoverContent,
  PopoverTrigger,
  RadioGroup,
  RadioGroupItem,
  SegmentedControl,
} from "@/ui";
import { AnimatedBoardSummary } from "./BoardChromeSummary";
import { useBoardFilterGeneration } from "./BoardFilterGenerationRuntime";

const boardSortFields = ["updated", "created", "labels", "title", "short_id"] as const satisfies readonly BoardNodeCardsSortField[];

export function BoardSortChrome() {
  const { t } = useTranslation();
  const generation = useBoardFilterGeneration();
  const selectedSort = generation.snapshot.desiredSort ?? generation.snapshot.active.sort;
  const active = !boardNodeCardsSortEqual(selectedSort, defaultBoardNodeCardsSort);
  const summary = active
    ? t("board.sortSummary", {
        direction: sortDirectionLabel(t, selectedSort.direction),
        field: sortFieldLabel(t, selectedSort.field),
      })
    : t("board.sort");
  return (
    <Popover>
      <PopoverTrigger asChild>
        <InteractiveChip
          aria-label={summary}
          selected={active}
          tone={active ? "primary" : "neutral"}
        >
          <ArrowUpDown aria-hidden="true" className="shrink-0" size={14} strokeWidth={1.8} />
          <AnimatedBoardSummary text={summary} />
        </InteractiveChip>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="grid w-[min(18rem,calc(100vw-24px))] gap-[var(--space-3)] p-[var(--space-3)]"
        collisionPadding={12}
        level={3}
      >
        <SegmentedControl
          ariaLabel={t("board.sortDirection")}
          onValueChange={(direction) => {
            generation.controller.setDesiredSort({
              direction,
              field: selectedSort.field,
            });
          }}
          options={[
            { label: t("board.sortAsc"), value: "asc" },
            { label: t("board.sortDesc"), value: "desc" },
          ]}
          value={selectedSort.direction}
        />
        <RadioGroup
          aria-label={t("board.sortField")}
          onValueChange={(field) => {
            generation.controller.setDesiredSort({
              direction: selectedSort.direction,
              field: boardSortField(field),
            });
          }}
          value={selectedSort.field}
        >
          {boardSortFields.map((field) => (
            <label
              className="flex min-h-8 items-center gap-[var(--space-2)] rounded-[var(--radius-s)] px-[var(--space-1)] text-sm text-[var(--color-on-island)]"
              key={field}
            >
              <RadioGroupItem value={field} />
              <span>{sortFieldLabel(t, field)}</span>
            </label>
          ))}
        </RadioGroup>
      </PopoverContent>
    </Popover>
  );
}

function boardSortField(value: string): BoardNodeCardsSortField {
  const field = boardSortFields.find((candidate) => candidate === value);
  if (field === undefined) {
    throw new Error(`Board Sort received unknown field "${value}".`);
  }
  return field;
}

function boardNodeCardsSortEqual(left: BoardNodeCardsSort, right: BoardNodeCardsSort): boolean {
  return left.field === right.field && left.direction === right.direction;
}

function sortFieldLabel(t: ReturnType<typeof useTranslation>["t"], field: BoardNodeCardsSortField): string {
  switch (field) {
    case "updated":
      return t("board.sortUpdated");
    case "created":
      return t("board.sortCreated");
    case "labels":
      return t("board.sortLabels");
    case "title":
      return t("board.sortTitle");
    case "short_id":
      return t("board.sortShortID");
  }
}

function sortDirectionLabel(
  t: ReturnType<typeof useTranslation>["t"],
  direction: BoardNodeCardsSort["direction"],
): string {
  return direction === "asc" ? t("board.sortAsc") : t("board.sortDesc");
}
