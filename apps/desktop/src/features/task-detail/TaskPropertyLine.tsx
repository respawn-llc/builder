import type { ReactNode } from "react";

import { cx } from "@/ui";

export function TaskPropertyLine({
  label,
  singleLine = false,
  value,
  valueClassName,
}: Readonly<{
  label: string;
  singleLine?: boolean | undefined;
  value: ReactNode;
  valueClassName?: string | undefined;
}>) {
  return (
    <div
      className={cx(
        "m-0 flex min-w-0 gap-[var(--space-1)] text-sm",
        singleLine ? "flex-nowrap items-baseline" : "flex-wrap items-start",
      )}
    >
      <dt
        aria-label={label}
        className={cx("after:content-[':']", singleLine && "min-w-0 truncate whitespace-nowrap")}
        title={singleLine ? label : undefined}
      >
        {label}
      </dt>
      <dd
        aria-label={`${label} value`}
        className={cx(
          "m-0 min-w-0 text-[var(--color-muted)]",
          singleLine && "shrink-0 whitespace-nowrap",
          valueClassName,
        )}
      >
        {value}
      </dd>
    </div>
  );
}
