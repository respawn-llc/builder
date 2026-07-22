import * as RadioGroupPrimitive from "@radix-ui/react-radio-group";
import type { CSSProperties, ReactNode } from "react";

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
  let selectedSegment: Readonly<{
    index: number;
    option: SegmentedControlOption<Value>;
  }> | null = null;
  for (const [index, option] of options.entries()) {
    if (optionValues.has(option.value)) {
      throw new Error(`Segmented control "${ariaLabel}" contains duplicate value "${option.value}".`);
    }
    optionValues.add(option.value);
    if (option.value === value) {
      selectedSegment = { index, option };
    }
  }
  if (selectedSegment === null) {
    throw new Error(`Segmented control "${ariaLabel}" has no option for value "${value}".`);
  }
  if (selectedSegment.option.disabled === true) {
    throw new Error(`Segmented control "${ariaLabel}" cannot select disabled value "${value}".`);
  }
  const indicatorStyle = {
    transform: `translateX(${(selectedSegment.index * 100).toString()}%)`,
    width: `calc((100% - (var(--space-1) * 2)) / ${options.length.toString()})`,
  } satisfies CSSProperties;
  return (
    <RadioGroupPrimitive.Root
      aria-label={ariaLabel}
      className={cx(
        "app-region-no-drag relative inline-grid h-[var(--space-6)] min-w-0 grid-flow-col auto-cols-fr rounded-[var(--radius-m)] border border-[var(--color-outline)] bg-[var(--color-island-1)] p-[var(--space-1)]",
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
      <span
        aria-hidden="true"
        className="pointer-events-none absolute top-[var(--space-1)] bottom-[var(--space-1)] left-[var(--space-1)] rounded-[calc(var(--radius-m)-var(--space-1))] bg-[var(--color-island-3)] transition-[transform,width] duration-[var(--motion-fast)] ease-out motion-reduce:transition-none"
        style={indicatorStyle}
      />
      {options.map((option) => (
        <RadioGroupPrimitive.Item
          className="relative z-10 h-full min-h-0 min-w-9 rounded-[calc(var(--radius-m)-var(--space-1))] bg-transparent px-[var(--space-2)] text-xs font-extrabold text-[var(--color-muted)] outline-none transition-[color,opacity] duration-[var(--motion-fast)] motion-reduce:transition-none hover:text-[var(--color-on-island)] focus-visible:ring-[3px] focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_40%,transparent)] disabled:cursor-not-allowed disabled:opacity-45 data-[state=checked]:text-[var(--color-on-island)] [@media(pointer:coarse)]:min-w-11"
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
