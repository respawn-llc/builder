import type { ReactNode } from "react";

import { cx } from "./classes";

export function CopyableValueButton({
  accessibleLabel,
  children,
  className,
  onActivate,
}: Readonly<{
  accessibleLabel: string;
  children: ReactNode;
  className?: string;
  onActivate: () => void;
}>) {
  return (
    <button
      aria-label={accessibleLabel}
      className={cx(
        "-mx-[var(--space-1)] min-w-0 whitespace-pre-wrap rounded-[var(--radius-m)] px-[var(--space-1)] py-[var(--space-1)] text-left text-sm text-[var(--color-muted)] transition-colors hover:bg-[var(--color-island-2)] hover:text-[var(--color-on-island)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--color-primary)]",
        className,
      )}
      onClick={onActivate}
      type="button"
    >
      {children}
    </button>
  );
}
