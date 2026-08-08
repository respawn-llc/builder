import { ArrowDownUpIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import type { BoardNodeCardsSort } from "@/api";
import {
  InteractiveChip,
  Popover,
  PopoverContent,
  PopoverTrigger,
  RadioGroup,
  RadioGroupItem,
  SegmentedControl,
} from "@/ui";
import { AnimatedBoardChipSummary } from "./BoardChipSummary";
import { useBoardFilterGeneration } from "./BoardFilterGenerationRuntime";

export const boardSortFieldOptions = [
  { value: "updated", labelKey: "board.sort.fields.updated" },
  { value: "created", labelKey: "board.sort.fields.created" },
  { value: "labels", labelKey: "board.sort.fields.labels" },
  { value: "short_id", labelKey: "board.sort.fields.short_id" },
] as const satisfies readonly Readonly<{
  value: BoardNodeCardsSort["field"];
  labelKey: string;
}>[];

const boardSortDirectionOptions = [
  { value: "asc", labelKey: "board.sort.directions.asc" },
  { value: "desc", labelKey: "board.sort.directions.desc" },
] as const satisfies readonly Readonly<{
  value: BoardNodeCardsSort["direction"];
  labelKey: string;
}>[];

export function BoardSortChrome() {
  const { t } = useTranslation();
  const { setSort, sort } = useBoardFilterGeneration();
  const [open, setOpen] = useState(false);
  const isDefault = sort.field === "updated" && sort.direction === "desc";
  const fieldOption = boardSortFieldOptions.find((option) => option.value === sort.field);
  const directionOption = boardSortDirectionOptions.find((option) => option.value === sort.direction);
  if (fieldOption === undefined || directionOption === undefined) {
    throw new Error("Board sort selection is not represented by the board sort options.");
  }
  const summary = isDefault
    ? t("board.sort.chip")
    : t("board.sort.summary", {
        field: t(fieldOption.labelKey),
        direction: t(directionOption.labelKey),
      });

  return (
    <Popover onOpenChange={setOpen} open={open}>
      <PopoverTrigger asChild>
        <InteractiveChip aria-label={summary} selected={!isDefault} tone={!isDefault ? "primary" : "neutral"}>
          <ArrowDownUpIcon aria-hidden="true" className="shrink-0" size={14} strokeWidth={1.8} />
          <AnimatedBoardChipSummary text={summary} />
        </InteractiveChip>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-64" level={4}>
        <RadioGroup
          aria-label={t("board.sort.fields.label")}
          onValueChange={(value) => {
            const option = boardSortFieldOptions.find((candidate) => candidate.value === value);
            if (option === undefined) {
              throw new Error(`Unknown Board sort field "${value}".`);
            }
            setSort({ field: option.value, direction: sort.direction });
          }}
          value={sort.field}
        >
          {boardSortFieldOptions.map((option) => (
            <label className="flex cursor-pointer items-center gap-[var(--space-2)]" key={option.value}>
              <RadioGroupItem aria-label={t(option.labelKey)} value={option.value} />
              <span>{t(option.labelKey)}</span>
            </label>
          ))}
        </RadioGroup>
        <SegmentedControl
          ariaLabel={t("board.sort.directions.label")}
          onValueChange={(direction) => {
            setSort({ field: sort.field, direction });
          }}
          options={boardSortDirectionOptions.map((option) => ({
            label: t(option.labelKey),
            value: option.value,
          }))}
          value={sort.direction}
        />
      </PopoverContent>
    </Popover>
  );
}
