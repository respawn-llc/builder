import type { ReactNode } from "react";

import { cn } from "./classes";
import { labelChipHeightClassName } from "./InteractiveChip";

export type BadgeTone = "neutral" | "info" | "success" | "warning" | "danger";

export type BadgeProps = Readonly<{
  children: ReactNode;
  className?: string;
  size?: "compact" | "default";
  tone?: BadgeTone;
  title?: string;
}>;

export function Badge({ children, className, size = "default", tone = "neutral", title }: BadgeProps) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border border-[var(--color-outline)] text-[var(--color-muted)]",
        size === "compact"
          ? "px-[6px] py-[2px] text-[11px] font-medium leading-3"
          : `${labelChipHeightClassName} px-[10px] font-extrabold`,
        tone === "info" && "text-[var(--color-primary)]",
        tone === "success" && "text-[var(--color-success)]",
        tone === "warning" && "text-[var(--color-warning)]",
        tone === "danger" && "text-[var(--color-error)]",
        className,
      )}
      title={title}
    >
      {children}
    </span>
  );
}
