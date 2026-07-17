import { cn } from "../classes";
import { islandSurfaceClassName, type IslandLevel } from "../islandSurfaceStyles";

export type RadixIslandSurfaceContentClassNameOptions = Readonly<{
  className?: string | undefined;
  level?: IslandLevel | undefined;
  noDrag?: boolean | undefined;
  originClassName: string;
}>;

const radixIslandSurfaceMotionClassName =
  "data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 data-[state=delayed-open]:animate-in data-[state=delayed-open]:fade-in-0 data-[state=delayed-open]:zoom-in-95 data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 data-open:animate-in data-open:fade-in-0 data-open:zoom-in-95 data-closed:animate-out data-closed:fade-out-0 data-closed:zoom-out-95";

export function radixIslandSurfaceContentClassName({
  className,
  level = 2,
  noDrag = true,
  originClassName,
}: RadixIslandSurfaceContentClassNameOptions): string {
  return cn(
    islandSurfaceClassName(level),
    noDrag && "app-region-no-drag",
    "z-50 outline-none",
    originClassName,
    radixIslandSurfaceMotionClassName,
    className,
  );
}
