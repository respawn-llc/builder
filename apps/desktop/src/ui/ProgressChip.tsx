import type { ChipProps, InteractiveChipProps } from "./InteractiveChip";
import { Chip, InteractiveChip } from "./InteractiveChip";

type ProgressProps = Readonly<{
  label: string;
  maximum: number;
  value: number;
}>;

export type ProgressChipProps = Readonly<Omit<ChipProps, "aria-label" | "children"> & ProgressProps>;

export type ProgressInteractiveChipProps = Readonly<
  Omit<InteractiveChipProps, "aria-label" | "children"> & ProgressProps
>;

export function ProgressChip({ label, maximum, size = "label", value, ...appearance }: ProgressChipProps) {
  validateProgress(maximum, value);
  return (
    <Chip aria-label={label} size={size} {...appearance}>
      <ProgressChipContent label={label} maximum={maximum} size={size} value={value} />
    </Chip>
  );
}

export function ProgressInteractiveChip({
  label,
  maximum,
  size = "label",
  value,
  ...behavior
}: ProgressInteractiveChipProps) {
  validateProgress(maximum, value);
  return (
    <InteractiveChip aria-label={label} size={size} {...behavior}>
      <ProgressChipContent label={label} maximum={maximum} size={size} value={value} />
    </InteractiveChip>
  );
}

function validateProgress(maximum: number, value: number): void {
  if (!Number.isInteger(maximum) || maximum <= 0) {
    throw new Error(
      `ProgressInteractiveChip maximum must be a positive integer; received ${String(maximum)}.`,
    );
  }
  if (!Number.isInteger(value) || value < 0 || value > maximum) {
    throw new Error(
      `ProgressInteractiveChip value must be an integer from 0 through ${String(maximum)}; received ${String(value)}.`,
    );
  }
}

function ProgressChipContent({
  label,
  maximum,
  size,
  value,
}: ProgressProps & Readonly<{ size: "compact" | "default" | "label" }>) {
  const circumference = 2 * Math.PI * 7;
  const completedLength = circumference * (value / maximum);
  return (
    <span className="inline-flex min-w-0 items-center gap-[var(--space-1)]">
      <svg
        aria-label={label}
        aria-valuemax={maximum}
        aria-valuemin={0}
        aria-valuenow={value}
        className={size === "compact" ? "size-[13px] shrink-0" : "size-4 shrink-0"}
        role="progressbar"
        viewBox="0 0 18 18"
      >
        <circle
          className="text-[color-mix(in_srgb,currentColor_25%,transparent)]"
          cx="9"
          cy="9"
          fill="none"
          r="7"
          stroke="currentColor"
          strokeWidth="2"
        />
        <circle
          className="origin-center -rotate-90 text-current"
          cx="9"
          cy="9"
          fill="none"
          r="7"
          stroke="currentColor"
          strokeDasharray={`${completedLength.toString()} ${circumference.toString()}`}
          strokeLinecap="round"
          strokeWidth="2"
        />
      </svg>
      <span aria-hidden="true" className="whitespace-nowrap">
        {value} / {maximum}
      </span>
    </span>
  );
}
