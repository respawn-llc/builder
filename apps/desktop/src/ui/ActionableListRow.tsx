import type { ButtonHTMLAttributes, HTMLAttributes, ReactNode } from "react";

import { cx } from "./classes";

type ActionableListRowSelection =
  | Readonly<{
      children: ReactNode;
      selectionControl?: undefined;
      selectButtonProps?: Omit<ButtonHTMLAttributes<HTMLButtonElement>, "children" | "className" | "type">;
    }>
  | Readonly<{
      children?: undefined;
      selectionControl: ReactNode;
      selectButtonProps?: undefined;
    }>;

export type ActionableListRowProps = Readonly<{
  actions?: ReactNode;
  className?: string | undefined;
  contextualActions?: ReactNode;
  leadingActions?: ReactNode;
  selected?: boolean;
}> &
  ActionableListRowSelection &
  Omit<HTMLAttributes<HTMLDivElement>, "children" | "className">;

export function ActionableListRow({
  actions,
  children,
  className,
  contextualActions,
  leadingActions,
  selected = false,
  selectionControl,
  selectButtonProps,
  ...props
}: ActionableListRowProps) {
  return (
    <div
      className={cx(
        "group/actionable-row app-region-no-drag relative flex min-h-9 w-full items-center rounded-[var(--radius-s)] border border-transparent bg-transparent transition-colors duration-100 motion-reduce:transition-none focus-within:bg-[var(--color-island-1)] data-[selected=true]:bg-[var(--color-island-2)] [@media(pointer:coarse)]:min-h-11",
        className,
      )}
      data-selected={selected}
      {...props}
    >
      {leadingActions === undefined ? null : (
        <div className="flex shrink-0 items-center gap-[var(--space-1)]">{leadingActions}</div>
      )}
      {selectionControl === undefined ? (
        <button
          aria-pressed={selected}
          className="min-w-0 flex-1 self-stretch rounded-[var(--radius-s)] px-[var(--space-2)] text-left text-sm text-[var(--color-on-island)] outline-none focus-visible:ring-[3px] focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_40%,transparent)] disabled:cursor-not-allowed disabled:opacity-45 [@media(pointer:coarse)]:px-[var(--space-3)]"
          type="button"
          {...selectButtonProps}
        >
          {children}
        </button>
      ) : (
        selectionControl
      )}
      {actions === undefined && contextualActions === undefined ? null : (
        <div className="ml-auto flex shrink-0 items-center gap-[var(--space-1)]">
          {contextualActions === undefined ? null : (
            <div className="pointer-events-none flex items-center gap-[var(--space-1)] opacity-0 transition-opacity duration-100 motion-reduce:transition-none group-hover/actionable-row:pointer-events-auto group-hover/actionable-row:opacity-100 group-focus-within/actionable-row:pointer-events-auto group-focus-within/actionable-row:opacity-100 [@media(pointer:coarse)]:pointer-events-auto [@media(pointer:coarse)]:opacity-100">
              {contextualActions}
            </div>
          )}
          {actions}
        </div>
      )}
    </div>
  );
}
