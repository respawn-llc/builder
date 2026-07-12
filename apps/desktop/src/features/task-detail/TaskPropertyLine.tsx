import type { ReactNode } from "react";

export function TaskPropertyLine({
  label,
  value,
}: Readonly<{ label: string; value: ReactNode }>) {
  return (
    <div className="m-0 flex min-w-0 flex-wrap items-center gap-[var(--space-1)] text-sm">
      <dt aria-label={label} className="after:content-[':']">{label}</dt>
      <dd aria-label={`${label} value`} className="m-0 min-w-0 text-[var(--color-muted)]">{value}</dd>
    </div>
  );
}
