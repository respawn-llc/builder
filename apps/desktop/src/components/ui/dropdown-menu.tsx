"use client";

import * as React from "react";
import * as DropdownMenuPrimitive from "@radix-ui/react-dropdown-menu";
import { Check, ChevronRight } from "lucide-react";

import { cn } from "@/lib/utils";
import { radixIslandSurfaceContentClassName } from "./radix-island-surface";
import type { IslandLevel } from "../../ui/islandSurfaceStyles";

function DropdownMenu({ ...props }: React.ComponentProps<typeof DropdownMenuPrimitive.Root>) {
  return <DropdownMenuPrimitive.Root data-slot="dropdown-menu" {...props} />;
}

function DropdownMenuTrigger({ ...props }: React.ComponentProps<typeof DropdownMenuPrimitive.Trigger>) {
  return <DropdownMenuPrimitive.Trigger data-slot="dropdown-menu-trigger" {...props} />;
}

function DropdownMenuGroup({ ...props }: React.ComponentProps<typeof DropdownMenuPrimitive.Group>) {
  return <DropdownMenuPrimitive.Group data-slot="dropdown-menu-group" {...props} />;
}

function DropdownMenuRadioGroup({ ...props }: React.ComponentProps<typeof DropdownMenuPrimitive.RadioGroup>) {
  return <DropdownMenuPrimitive.RadioGroup data-slot="dropdown-menu-radio-group" {...props} />;
}

function DropdownMenuSub({ ...props }: React.ComponentProps<typeof DropdownMenuPrimitive.Sub>) {
  return <DropdownMenuPrimitive.Sub data-slot="dropdown-menu-sub" {...props} />;
}

function DropdownMenuSubTrigger({
  className,
  inset,
  children,
  ...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.SubTrigger> &
  Readonly<{ inset?: boolean | undefined }>) {
  return (
    <DropdownMenuPrimitive.SubTrigger
      className={cn(dropdownMenuItemClassName, inset && "pl-8", className)}
      data-slot="dropdown-menu-sub-trigger"
      {...props}
    >
      {children}
      <ChevronRight aria-hidden="true" className="ml-auto size-4 shrink-0" strokeWidth={1.7} />
    </DropdownMenuPrimitive.SubTrigger>
  );
}

function DropdownMenuSubContent({
  className,
  level,
  sideOffset = 6,
  ...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.SubContent> &
  Readonly<{ level?: IslandLevel | undefined }>) {
  return (
    <DropdownMenuPrimitive.SubContent
      className={cn(dropdownMenuContentClassName(level), className)}
      data-slot="dropdown-menu-sub-content"
      sideOffset={sideOffset}
      {...props}
    />
  );
}

function DropdownMenuContent({
  align = "start",
  className,
  collisionPadding = 12,
  level,
  sideOffset = 6,
  ...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.Content> &
  Readonly<{ level?: IslandLevel | undefined }>) {
  return (
    <DropdownMenuPrimitive.Portal>
      <DropdownMenuPrimitive.Content
        align={align}
        className={cn(dropdownMenuContentClassName(level), className)}
        collisionPadding={collisionPadding}
        data-slot="dropdown-menu-content"
        sideOffset={sideOffset}
        {...props}
      />
    </DropdownMenuPrimitive.Portal>
  );
}

function DropdownMenuItem({
  className,
  inset,
  variant = "default",
  ...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.Item> &
  Readonly<{ inset?: boolean | undefined; variant?: "default" | "danger" | undefined }>) {
  return (
    <DropdownMenuPrimitive.Item
      className={cn(
        dropdownMenuItemClassName,
        inset && "pl-8",
        variant === "danger" &&
          "text-[var(--color-error)] data-[highlighted]:border-[var(--color-error)] data-[highlighted]:text-[var(--color-error)]",
        className,
      )}
      data-slot="dropdown-menu-item"
      {...props}
    />
  );
}

function DropdownMenuCheckboxItem({
  className,
  children,
  checked,
  ...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.CheckboxItem>) {
  const checkedProps = checked === undefined ? {} : { checked };
  return (
    <DropdownMenuPrimitive.CheckboxItem
      className={cn(dropdownMenuItemClassName, "pl-8", className)}
      data-slot="dropdown-menu-checkbox-item"
      {...checkedProps}
      {...props}
    >
      <span className="absolute left-[var(--space-2)] grid size-4 place-items-center">
        <DropdownMenuPrimitive.ItemIndicator>
          <Check aria-hidden="true" size={16} strokeWidth={1.7} />
        </DropdownMenuPrimitive.ItemIndicator>
      </span>
      {children}
    </DropdownMenuPrimitive.CheckboxItem>
  );
}

function DropdownMenuRadioItem({
  className,
  children,
  ...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.RadioItem>) {
  return (
    <DropdownMenuPrimitive.RadioItem
      className={cn(dropdownMenuItemClassName, "pl-8", className)}
      data-slot="dropdown-menu-radio-item"
      {...props}
    >
      <span className="absolute left-[var(--space-2)] grid size-4 place-items-center text-[var(--color-primary)]">
        <DropdownMenuPrimitive.ItemIndicator>
          <Check aria-hidden="true" size={16} strokeWidth={1.7} />
        </DropdownMenuPrimitive.ItemIndicator>
      </span>
      {children}
    </DropdownMenuPrimitive.RadioItem>
  );
}

function DropdownMenuLabel({
  className,
  inset,
  ...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.Label> & Readonly<{ inset?: boolean | undefined }>) {
  return (
    <DropdownMenuPrimitive.Label
      className={cn(
        "px-[var(--space-3)] py-[var(--space-2)] text-sm font-bold text-[var(--color-on-island)] opacity-70",
        inset && "pl-8",
        className,
      )}
      data-slot="dropdown-menu-label"
      {...props}
    />
  );
}

function DropdownMenuSeparator({
  className,
  ...props
}: React.ComponentProps<typeof DropdownMenuPrimitive.Separator>) {
  return (
    <DropdownMenuPrimitive.Separator
      className={cn("my-[var(--space-1)] h-px bg-[var(--color-outline)]", className)}
      data-slot="dropdown-menu-separator"
      {...props}
    />
  );
}

function DropdownMenuShortcut({ className, ...props }: React.ComponentProps<"span">) {
  return (
    <span
      className={cn("ml-auto text-xs tracking-widest text-[var(--color-muted)]", className)}
      data-slot="dropdown-menu-shortcut"
      {...props}
    />
  );
}

const dropdownMenuItemClassName =
  "relative flex min-h-9 cursor-default select-none items-center gap-[var(--space-2)] rounded-[var(--radius-m)] border border-transparent px-[var(--space-3)] py-[var(--space-2)] text-sm font-semibold outline-none transition-colors data-[disabled]:cursor-not-allowed data-[disabled]:opacity-50 data-[highlighted]:border-[var(--color-primary)] data-[highlighted]:bg-[var(--color-island-2)]";

function dropdownMenuContentClassName(level: IslandLevel | undefined): string {
  return cn(
    radixIslandSurfaceContentClassName({
      level,
      originClassName: "origin-(--radix-dropdown-menu-content-transform-origin)",
    }),
    "grid max-h-[var(--radix-dropdown-menu-content-available-height)] min-w-56 gap-[var(--space-1)] overflow-y-auto overflow-x-hidden rounded-[var(--radius-l)] p-[var(--space-2)] text-[var(--color-on-island)]",
  );
}

export {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
};
