"use client";

import * as React from "react";
import * as PopoverPrimitive from "@radix-ui/react-popover";

import { cn } from "../classes";
import { motionDurationFromCSSVar } from "../motion";
import { radixIslandSurfaceContentClassName } from "./radix-island-surface";
import type { IslandLevel } from "../islandSurfaceStyles";

const motionMorphVarName = "--motion-morph";
const fallbackMotionMorphMs = 350;

function Popover({ ...props }: React.ComponentProps<typeof PopoverPrimitive.Root>) {
  return <PopoverPrimitive.Root data-slot="popover" {...props} />;
}

function PopoverTrigger({ ...props }: React.ComponentProps<typeof PopoverPrimitive.Trigger>) {
  return <PopoverPrimitive.Trigger data-slot="popover-trigger" {...props} />;
}

function PopoverContent({
  align = "center",
  className,
  level,
  ref,
  sideOffset = 8,
  ...props
}: React.ComponentProps<typeof PopoverPrimitive.Content> & Readonly<{ level?: IslandLevel | undefined }>) {
  const contentRef = React.useRef<HTMLDivElement | null>(null);
  React.useImperativeHandle(ref, () => {
    const element = contentRef.current;
    if (element === null) {
      throw new Error("Popover content ref is unavailable after mount.");
    }
    return element;
  }, []);
  usePopoverHeightMotion(contentRef);
  return (
    <PopoverPrimitive.Portal>
      <PopoverPrimitive.Content
        align={align}
        className={cn(
          radixIslandSurfaceContentClassName({
            level,
            originClassName: "origin-(--radix-popover-content-transform-origin)",
          }),
          "grid w-64 gap-[var(--space-3)] rounded-[var(--radius-l)] p-[var(--space-3)] text-[var(--color-on-island)]",
          className,
        )}
        data-slot="popover-content"
        ref={contentRef}
        sideOffset={sideOffset}
        {...props}
      />
    </PopoverPrimitive.Portal>
  );
}

function usePopoverHeightMotion(ref: React.RefObject<HTMLDivElement | null>): void {
  React.useLayoutEffect(() => {
    const element = ref.current;
    if (element === null || typeof ResizeObserver === "undefined") {
      return undefined;
    }
    let previousHeight: number | null = null;
    let animation: Animation | null = null;
    let targetHeight: number | null = null;
    let overflowBeforeAnimation: string | null = null;
    const observer = new ResizeObserver(([entry]) => {
      if (entry === undefined || animation !== null) {
        return;
      }
      const nextHeight = entry.borderBoxSize[0]?.blockSize ?? entry.contentRect.height;
      if (previousHeight === null || Math.abs(nextHeight - previousHeight) < 1) {
        previousHeight = nextHeight;
        return;
      }
      const duration = motionDurationFromCSSVar(motionMorphVarName, fallbackMotionMorphMs);
      if (duration === 0) {
        previousHeight = nextHeight;
        return;
      }
      targetHeight = nextHeight;
      overflowBeforeAnimation = element.style.overflow;
      element.style.overflow = "hidden";
      animation = element.animate(
        [{ height: `${previousHeight.toString()}px` }, { height: `${nextHeight.toString()}px` }],
        { duration, easing: "ease-in-out" },
      );
      void animation.finished
        .catch(() => undefined)
        .then(() => {
          previousHeight = targetHeight;
          targetHeight = null;
          animation = null;
          element.style.overflow = overflowBeforeAnimation ?? "";
          overflowBeforeAnimation = null;
        });
    });
    observer.observe(element, { box: "border-box" });
    return () => {
      observer.disconnect();
      animation?.cancel();
      if (overflowBeforeAnimation !== null) {
        element.style.overflow = overflowBeforeAnimation;
      }
    };
  }, [ref]);
}

function PopoverClose({ ...props }: React.ComponentProps<typeof PopoverPrimitive.Close>) {
  return <PopoverPrimitive.Close data-slot="popover-close" {...props} />;
}

export { Popover, PopoverClose, PopoverContent, PopoverTrigger };
