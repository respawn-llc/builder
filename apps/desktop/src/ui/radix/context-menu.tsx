"use client";

import * as React from "react";
import * as ContextMenuPrimitive from "@radix-ui/react-context-menu";

import { cn } from "../classes";
import { radixIslandSurfaceContentClassName } from "./radix-island-surface";
import type { IslandLevel } from "../islandSurfaceStyles";

function ContextMenu({ ...props }: React.ComponentProps<typeof ContextMenuPrimitive.Root>) {
  return <ContextMenuPrimitive.Root data-slot="context-menu" {...props} />;
}

function ContextMenuTrigger({ ...props }: React.ComponentProps<typeof ContextMenuPrimitive.Trigger>) {
  return <ContextMenuPrimitive.Trigger data-slot="context-menu-trigger" {...props} />;
}

function ContextMenuContent({
  className,
  level,
  ...props
}: React.ComponentProps<typeof ContextMenuPrimitive.Content> &
  Readonly<{ level?: IslandLevel | undefined }>) {
  return (
    <ContextMenuPrimitive.Portal>
      <ContextMenuPrimitive.Content
        className={cn(
          radixIslandSurfaceContentClassName({
            level,
            originClassName: "origin-(--radix-context-menu-content-transform-origin)",
          }),
          "grid min-w-56 gap-[var(--space-1)] overflow-hidden rounded-[var(--radius-l)] p-[var(--space-2)] text-[var(--color-on-island)]",
          className,
        )}
        data-slot="context-menu-content"
        {...props}
      />
    </ContextMenuPrimitive.Portal>
  );
}

function ContextMenuItem({ className, ...props }: React.ComponentProps<typeof ContextMenuPrimitive.Item>) {
  return (
    <ContextMenuPrimitive.Item
      className={cn(
        "relative flex cursor-default select-none items-center rounded-[var(--radius-m)] border border-transparent px-[var(--space-3)] py-[var(--space-2)] text-sm font-semibold outline-none transition-colors data-[disabled]:pointer-events-none data-[disabled]:opacity-50 data-[highlighted]:border-[var(--color-primary)] data-[highlighted]:bg-[var(--color-island-2)]",
        className,
      )}
      data-slot="context-menu-item"
      {...props}
    />
  );
}

function ContextMenuSeparator({
  className,
  ...props
}: React.ComponentProps<typeof ContextMenuPrimitive.Separator>) {
  return (
    <ContextMenuPrimitive.Separator
      className={cn("my-[var(--space-1)] h-px bg-[var(--color-outline)]", className)}
      data-slot="context-menu-separator"
      {...props}
    />
  );
}

export { ContextMenu, ContextMenuContent, ContextMenuItem, ContextMenuSeparator, ContextMenuTrigger };
