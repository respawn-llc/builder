import * as React from "react";
import * as TooltipPrimitive from "@radix-ui/react-tooltip";

import { cn } from "../classes";
import { radixIslandSurfaceContentClassName } from "./radix-island-surface";
import type { IslandLevel } from "../islandSurfaceStyles";

function TooltipProvider({
  delayDuration = 0,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Provider>) {
  return <TooltipPrimitive.Provider data-slot="tooltip-provider" delayDuration={delayDuration} {...props} />;
}

function Tooltip({ ...props }: React.ComponentProps<typeof TooltipPrimitive.Root>) {
  return <TooltipPrimitive.Root data-slot="tooltip" {...props} />;
}

function TooltipTrigger({ ...props }: React.ComponentProps<typeof TooltipPrimitive.Trigger>) {
  return <TooltipPrimitive.Trigger data-slot="tooltip-trigger" {...props} />;
}

function TooltipContent({
  className,
  level = 0,
  showArrow = false,
  sideOffset = 0,
  children,
  ...props
}: React.ComponentProps<typeof TooltipPrimitive.Content> &
  Readonly<{ level?: IslandLevel | undefined; showArrow?: boolean | undefined }>) {
  return (
    <TooltipPrimitive.Portal>
      <TooltipPrimitive.Content
        data-slot="tooltip-content"
        sideOffset={sideOffset}
        className={cn(
          radixIslandSurfaceContentClassName({
            level,
            noDrag: false,
            originClassName: "origin-(--radix-tooltip-content-transform-origin)",
          }),
          "inline-flex w-fit max-w-xs items-center gap-1.5 rounded-[var(--radius-m)] px-3 py-1.5 text-xs text-[var(--color-on-island)] has-data-[slot=kbd]:pr-1.5 **:data-[slot=kbd]:relative **:data-[slot=kbd]:isolate **:data-[slot=kbd]:z-50 **:data-[slot=kbd]:rounded-sm",
          className,
        )}
        {...props}
      >
        {children}
        {showArrow ? (
          <TooltipPrimitive.Arrow
            className="z-50 size-2.5 translate-y-[calc(-50%_-_2px)] rotate-45 rounded-[2px] bg-[var(--color-island-0)] fill-[var(--color-island-0)]"
            data-slot="tooltip-arrow"
            data-testid="tooltip-arrow"
          />
        ) : null}
      </TooltipPrimitive.Content>
    </TooltipPrimitive.Portal>
  );
}

export { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger };
