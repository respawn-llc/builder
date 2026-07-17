import type { HTMLAttributes, ReactNode, Ref } from "react";

import { cx } from "./classes";
import { IslandSurface } from "./IslandSurface";

export type IslandTabItem<TValue extends string> = Readonly<{
  action?: IslandTabAction | undefined;
  label: ReactNode;
  meta?: ReactNode | undefined;
  testId?: string | undefined;
  value: TValue;
}>;

export type IslandTabAction = Readonly<{
  ariaLabel: string;
  children: ReactNode;
  disabled?: boolean | undefined;
  onClick: () => void;
}>;

export type IslandTabsProps<TValue extends string> = Readonly<{
  ariaLabel: string;
  containerRef?: Ref<HTMLDivElement> | undefined;
  items: readonly IslandTabItem<TValue>[];
  onValueChange: (value: TValue) => void;
  value: TValue;
}> &
  Omit<HTMLAttributes<HTMLDivElement>, "children" | "ref" | "role" | "aria-label" | "onChange">;

export function IslandTabs<TValue extends string>({
  ariaLabel,
  className,
  containerRef,
  items,
  onValueChange,
  value,
  ...props
}: IslandTabsProps<TValue>) {
  return (
    <div
      {...props}
      aria-label={ariaLabel}
      className={cx("grid gap-[var(--space-2)]", className)}
      ref={containerRef}
      role="tablist"
    >
      {items.map((item) => (
        <IslandTab
          active={item.value === value}
          item={item}
          key={item.value}
          onSelect={() => {
            onValueChange(item.value);
          }}
        />
      ))}
    </div>
  );
}

function IslandTab<TValue extends string>({
  active,
  item,
  onSelect,
}: Readonly<{
  active: boolean;
  item: IslandTabItem<TValue>;
  onSelect: () => void;
}>) {
  return (
    <IslandSurface
      className="pointer-events-auto grid grid-cols-[minmax(0,1fr)_auto] items-center gap-[var(--space-1)] rounded-full p-1"
      data-testid={item.testId}
      level={1}
    >
      <button
        aria-selected={active}
        className="flex min-w-0 items-center gap-[var(--space-2)] rounded-full px-[var(--space-3)] py-[var(--space-2)] text-left font-bold text-[var(--color-on-island)] transition-colors data-[active=true]:bg-[var(--color-island-2)]"
        data-active={active}
        onClick={onSelect}
        role="tab"
        type="button"
      >
        <span className="min-w-0 truncate">{item.label}</span>
        {item.meta !== undefined ? (
          <span className="shrink-0 text-xs text-[var(--color-muted)]">{item.meta}</span>
        ) : null}
      </button>
      {item.action !== undefined ? (
        <button
          aria-label={item.action.ariaLabel}
          className="grid h-8 w-8 place-items-center rounded-full border border-[var(--color-outline)] bg-[var(--color-island-2)] text-[var(--color-on-island)] disabled:cursor-not-allowed disabled:opacity-55"
          disabled={item.action.disabled}
          onClick={item.action.onClick}
          type="button"
        >
          {item.action.children}
        </button>
      ) : null}
    </IslandSurface>
  );
}
