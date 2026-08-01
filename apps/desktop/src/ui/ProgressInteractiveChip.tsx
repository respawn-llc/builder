import type { InteractiveChipProps } from "./InteractiveChip";
import { InteractiveChip } from "./InteractiveChip";

export type ProgressInteractiveChipProps = Readonly<
  Omit<InteractiveChipProps, "aria-label" | "children"> & {
    label: string;
    maximum: number;
    value: number;
  }
>;

export function ProgressInteractiveChip({
  label,
  maximum,
  size = "label",
  value,
  ...behavior
}: ProgressInteractiveChipProps) {
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
  const circumference = 2 * Math.PI * 7;
  const completedLength = circumference * (value / maximum);
  return (
    <InteractiveChip aria-label={label} size={size} {...behavior}>
      <span className="inline-flex min-w-0 items-center gap-[var(--space-1)]">
        <svg
          aria-label={label}
          aria-valuemax={maximum}
          aria-valuemin={0}
          aria-valuenow={value}
          className="size-4 shrink-0"
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
    </InteractiveChip>
  );
}
