import { ArrowDownUpIcon } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { AnimatedChipSummary } from "./AnimatedChipSummary";
import { InteractiveChip } from "./InteractiveChip";
import { Popover, PopoverContent, PopoverTrigger } from "./radix/popover";
import { RadioGroup, RadioGroupItem } from "./radix/radio-group";
import { SegmentedControl } from "./SegmentedControl";

type SortDirection = "asc" | "desc";

export function SortChrome<Field extends string>({
  fieldOptions,
  onSortChange,
  sort,
}: Readonly<{
  fieldOptions: readonly Readonly<{ labelKey: string; value: Field }>[];
  onSortChange(sort: Readonly<{ field: Field; direction: SortDirection }>): void;
  sort: Readonly<{ field: Field; direction: SortDirection }>;
}>) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const isDefault = sort.field === "updated" && sort.direction === "desc";
  const fieldOption = fieldOptions.find((option) => option.value === sort.field);
  const directionOption = sortDirectionOptions.find((option) => option.value === sort.direction);
  if (fieldOption === undefined || directionOption === undefined) {
    throw new Error("Sort selection is not represented by the available sort options.");
  }
  const summary = isDefault
    ? t("board.sort.chip")
    : t("board.sort.summary", {
        direction: t(directionOption.labelKey),
        field: t(fieldOption.labelKey),
      });

  return (
    <Popover onOpenChange={setOpen} open={open}>
      <PopoverTrigger asChild>
        <InteractiveChip aria-label={summary} selected={!isDefault} tone={!isDefault ? "primary" : "neutral"}>
          <ArrowDownUpIcon aria-hidden="true" className="shrink-0" size={14} strokeWidth={1.8} />
          <AnimatedChipSummary text={summary} />
        </InteractiveChip>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-64" level={4}>
        <RadioGroup
          aria-label={t("board.sort.fields.label")}
          onValueChange={(value) => {
            const option = fieldOptions.find((candidate) => candidate.value === value);
            if (option === undefined) {
              throw new Error(`Unknown sort field "${value}".`);
            }
            onSortChange({ direction: sort.direction, field: option.value });
          }}
          value={sort.field}
        >
          {fieldOptions.map((option) => (
            <label className="flex cursor-pointer items-center gap-[var(--space-2)]" key={option.value}>
              <RadioGroupItem aria-label={t(option.labelKey)} value={option.value} />
              <span>{t(option.labelKey)}</span>
            </label>
          ))}
        </RadioGroup>
        <SegmentedControl
          ariaLabel={t("board.sort.directions.label")}
          onValueChange={(direction) => {
            onSortChange({ direction, field: sort.field });
          }}
          options={sortDirectionOptions.map((option) => ({
            label: t(option.labelKey),
            value: option.value,
          }))}
          value={sort.direction}
        />
      </PopoverContent>
    </Popover>
  );
}

const sortDirectionOptions = [
  { value: "asc", labelKey: "board.sort.directions.asc" },
  { value: "desc", labelKey: "board.sort.directions.desc" },
] as const satisfies readonly Readonly<{
  labelKey: string;
  value: SortDirection;
}>[];
