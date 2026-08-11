import { cx } from "./classes";
import { islandSurfaceClassName, type IslandLevel } from "./islandSurfaceStyles";

const fieldInputBaseClassName =
  "app-region-no-drag w-full border border-[var(--color-outline)] bg-[var(--color-island-1)] px-[14px] py-3 text-[var(--color-on-island)] outline-none transition-[height,border-color,box-shadow,background-color] focus-visible:border-[var(--color-primary)] disabled:cursor-not-allowed disabled:opacity-55";

export type FieldIslandRadius = "m" | "l";

const fieldIslandRadiusClassNames: Record<FieldIslandRadius, string> = {
  m: "rounded-[var(--radius-m)]",
  l: "rounded-[var(--radius-l)]",
};

export const fieldInputClassName = cx(fieldInputBaseClassName, fieldIslandRadiusClassNames.m);

export function fieldIslandInputClassName(level: IslandLevel = 0, radius: FieldIslandRadius = "m"): string {
  return cx(fieldInputBaseClassName, fieldIslandRadiusClassNames[radius], islandSurfaceClassName(level));
}
