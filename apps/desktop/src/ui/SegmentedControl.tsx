import * as RadioGroupPrimitive from "@radix-ui/react-radio-group";
import type { ReactNode } from "react";

import { cx } from "./classes";

export type SegmentedControlOption<Value extends string> = Readonly<{
  disabled?: boolean;
  label: ReactNode;
  value: Value;
}>;

export type SegmentedControlProps<Value extends string> = Readonly<{
  ariaLabel: string;
  className?: string | undefined;
  disabled?: boolean;
  onValueChange(value: Value): void;
  options: readonly SegmentedControlOption<Value>[];
  value: Value;
}>;

export function SegmentedControl<Value extends string>({
  ariaLabel,
  className,
  disabled = false,
  onValueChange,
  options,
  value,
}: SegmentedControlProps<Value>) {
  const optionValues = new Set<Value>();
  let selectedOption: SegmentedControlOption<Value> | null = null;
  for (const option of options) {
    if (optionValues.has(option.value)) {
      throw new Error(`Segmented control "${ariaLabel}" contains duplicate value "${option.value}".`);
    }
    optionValues.add(option.value);
    if (option.value === value) {
      selectedOption = option;
    }
  }
  if (selectedOption === null) {
    throw new Error(`Segmented control "${ariaLabel}" has no option for value "${value}".`);
  }
  if (selectedOption.disabled === true) {
    throw new Error(`Segmented control "${ariaLabel}" cannot select disabled value "${value}".`);
  }
  return (
    <RadioGroupPrimitive.Root
      aria-label={ariaLabel}
      className={cx(
        "app-region-no-drag inline-flex min-w-0 rounded-[var(--radius-s)] border border-[var(--color-outline)] bg-[var(--color-island-1)] p-[var(--space-1)]",
        className,
      )}
      disabled={disabled}
      onValueChange={(nextValue) => {
        const option = options.find((candidate) => candidate.value === nextValue);
        if (option === undefined) {
          throw new Error(`Segmented control "${ariaLabel}" received unknown value "${nextValue}".`);
        }
        onValueChange(option.value);
      }}
      orientation="horizontal"
      value={value}
    >
      {options.map((option) => (
        <RadioGroupPrimitive.Item
          className="min-h-6 min-w-9 rounded-[calc(var(--radius-s)-var(--space-1))] px-[var(--space-2)] text-xs font-extrabold text-[var(--color-muted)] outline-none transition-[background-color,color,opacity] duration-100 motion-reduce:transition-none hover:text-[var(--color-on-island)] focus-visible:ring-[3px] focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_40%,transparent)] disabled:cursor-not-allowed disabled:opacity-45 data-[state=checked]:bg-[var(--color-island-3)] data-[state=checked]:text-[var(--color-on-island)] [@media(pointer:coarse)]:min-h-9 [@media(pointer:coarse)]:min-w-11"
          disabled={option.disabled}
          key={option.value}
          value={option.value}
        >
          {option.label}
        </RadioGroupPrimitive.Item>
      ))}
    </RadioGroupPrimitive.Root>
  );
}
