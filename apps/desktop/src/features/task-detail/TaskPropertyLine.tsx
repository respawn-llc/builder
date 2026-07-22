import type { ReactNode } from "react";

import { cx } from "@/ui";

export function TaskPropertyLine({
  label,
  value,
  valueClassName,
}: Readonly<{ label: string; value: ReactNode; valueClassName?: string | undefined }>) {
  return (
    <div className="m-0 flex min-w-0 flex-wrap items-start gap-[var(--space-1)] text-sm">
      <dt aria-label={label} className="after:content-[':']">
        {label}
      </dt>
      <dd
        aria-label={`${label} value`}
        className={cx("m-0 min-w-0 text-[var(--color-muted)]", valueClassName)}
      >
        {value}
      </dd>
    </div>
  );
}
