import type { ButtonHTMLAttributes, HTMLAttributes, ReactNode } from "react";

import { cx } from "./classes";

type ChipAppearanceProps = Readonly<{
  children: ReactNode;
  size?: InteractiveChipSize;
  selected?: boolean;
  tone?: InteractiveChipTone;
}>;

export type ChipProps = ChipAppearanceProps & HTMLAttributes<HTMLSpanElement>;

export type InteractiveChipProps = ChipAppearanceProps &
  ButtonHTMLAttributes<HTMLButtonElement>;

export type InteractiveChipSize = "compact" | "default";
export type InteractiveChipTone = "neutral" | "primary";

export function Chip({
  children,
  className,
  selected,
  size = "default",
  tone = "neutral",
  ...props
}: ChipProps) {
  return (
    <span
      className={cx(
        chipBaseClassName,
        interactiveChipSizeClassNames[size],
        chipToneClassNames[tone],
        className,
      )}
      data-selected={selected === true}
      {...props}
    >
      {children}
    </span>
  );
}

export function InteractiveChip({
  children,
  className,
  selected,
  size = "default",
  tone = "neutral",
  type = "button",
  ...props
}: InteractiveChipProps) {
  return (
    <button
      aria-pressed={selected}
      className={cx(
        chipBaseClassName,
        "outline-none transition-[background-color,border-color,color,opacity] duration-100 motion-reduce:transition-none focus-visible:ring-[3px] disabled:cursor-not-allowed disabled:opacity-45",
        interactiveChipSizeClassNames[size],
        chipToneClassNames[tone],
        interactiveChipToneClassNames[tone],
        className,
      )}
      data-selected={selected === true}
      type={type}
      {...props}
    >
      {children}
    </button>
  );
}

const chipBaseClassName =
  "app-region-no-drag inline-flex max-w-full items-center gap-[var(--space-1)] rounded-full border";

const interactiveChipSizeClassNames = {
  compact:
    "min-h-5 px-[var(--space-1)] text-[11px] font-semibold leading-4 [@media(pointer:coarse)]:min-h-9 [@media(pointer:coarse)]:px-[var(--space-2)]",
  default:
    "min-h-7 px-[var(--space-2)] text-sm font-medium [@media(pointer:coarse)]:min-h-9 [@media(pointer:coarse)]:px-[var(--space-3)]",
} satisfies Record<InteractiveChipSize, string>;

const interactiveChipToneClassNames = {
  neutral:
    "hover:bg-[var(--color-island-2)] focus-visible:border-[var(--color-primary)] focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_40%,transparent)]",
  primary:
    "hover:bg-[color-mix(in_srgb,var(--color-primary)_16%,transparent)] focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_35%,transparent)]",
} satisfies Record<InteractiveChipTone, string>;

const chipToneClassNames = {
  neutral:
    "border-[var(--color-outline)] bg-[var(--color-island-1)] text-[var(--color-muted)] data-[selected=true]:bg-[var(--color-island-2)] data-[selected=true]:text-[var(--color-on-island)]",
  primary:
    "border-[color-mix(in_srgb,var(--color-primary)_45%,transparent)] bg-[color-mix(in_srgb,var(--color-primary)_10%,transparent)] text-[var(--color-primary)] data-[selected=true]:bg-[color-mix(in_srgb,var(--color-primary)_18%,transparent)]",
} satisfies Record<InteractiveChipTone, string>;
