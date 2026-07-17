import type { ComponentProps } from "react";

import { cx } from "./classes";

// Local shadcn item adaptation. `shadcn add item` prompts for components.json in this package;
// use `pnpm dlx shadcn@latest view item` unless we intentionally add shadcn config.
export function ItemGroup({ className, ...props }: ComponentProps<"div">) {
  return (
    <div className={cx("group/item-group flex flex-col", className)} data-slot="item-group" {...props} />
  );
}

export function Item({ className, ...props }: ComponentProps<"button">) {
  return (
    <button
      className={cx(
        "group/item flex w-full flex-wrap items-center rounded-md border border-transparent bg-transparent text-left text-sm outline-none transition-colors duration-100 hover:bg-[var(--color-island-1)] focus-visible:border-[var(--color-primary)] focus-visible:ring-[3px] focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_40%,transparent)] disabled:cursor-not-allowed disabled:opacity-45",
        className,
      )}
      data-slot="item"
      type="button"
      {...props}
    />
  );
}

export function ItemContent({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      className={cx("flex flex-1 flex-col gap-1 [&+[data-slot=item-content]]:flex-none", className)}
      data-slot="item-content"
      {...props}
    />
  );
}

export function ItemTitle({ className, ...props }: ComponentProps<"div">) {
  return (
    <div
      className={cx("flex w-fit items-center gap-2 text-sm font-medium leading-snug", className)}
      data-slot="item-title"
      {...props}
    />
  );
}
