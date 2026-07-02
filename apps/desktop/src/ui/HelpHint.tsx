import { CircleQuestionMark, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { z } from "zod";

import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "../components/ui/tooltip";
import { cx } from "./classes";
import type { IslandLevel } from "./islandSurfaceStyles";

type HelpHintBaseProps = Readonly<{
  /**
   * Icon rendered in both the trigger and the tooltip. Defaults to a circled
   * question mark; pass any Lucide icon to customize.
   */
  icon?: LucideIcon | undefined;
  /** Tooltip placement relative to the trigger. */
  side?: "top" | "right" | "bottom" | "left" | undefined;
  /** Surface depth for the tooltip background. */
  level?: IslandLevel | undefined;
  /** Extra classes for the trigger hit area. */
  className?: string | undefined;
}>;

/**
 * `label` is the tooltip content shown on hover/focus. Pass already-resolved
 * copy (e.g. `t("some.key")`); the component stays i18n-agnostic so callers own
 * translation and interpolation. A string `label` doubles as the trigger's
 * accessible name (overridable via `ariaLabel`); when `label` is rich content
 * the trigger has no derivable name, so `ariaLabel` becomes required.
 */
export type HelpHintProps =
  | (HelpHintBaseProps & Readonly<{ label: string; ariaLabel?: string | undefined }>)
  | (HelpHintBaseProps & Readonly<{ label: ReactNode; ariaLabel: string }>);

/** Diameter of the rendered question/help glyph, per design spec. */
const ICON_SIZE = 14;
const accessibleLabelSchema = z.string();

/**
 * Faint circled help icon whose layout footprint is just the glyph. A
 * transparent 40x40 pseudo-element overflows the button to enlarge the
 * hit/hover target without affecting surrounding spacing. Hovering or focusing
 * the trigger fades the glyph to full strength and reveals the shared tooltip,
 * which repeats the icon alongside the help copy. Newlines in a string `label`
 * are preserved so multi-line hints lay out as written.
 */
export function HelpHint({
  ariaLabel,
  className,
  icon: Icon = CircleQuestionMark,
  label,
  level = 0,
  side = "top",
}: HelpHintProps) {
  const parsedLabel = accessibleLabelSchema.safeParse(label);
  const accessibleName = ariaLabel ?? (parsedLabel.success ? parsedLabel.data : undefined);

  return (
    <TooltipProvider delayDuration={0}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            aria-label={accessibleName}
            className={cx(
              "group relative inline-grid place-items-center rounded-full bg-transparent text-[var(--color-muted)] transition-colors before:absolute before:left-1/2 before:top-1/2 before:size-10 before:-translate-x-1/2 before:-translate-y-1/2 before:content-[''] hover:text-[var(--color-on-island)] focus-visible:text-[var(--color-on-island)] focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--color-primary)]",
              className,
            )}
            data-slot="help-hint-trigger"
            type="button"
          >
            <Icon aria-hidden="true" size={ICON_SIZE} strokeWidth={1.7} />
          </button>
        </TooltipTrigger>
        <TooltipContent className="max-w-xs items-start gap-2 text-[13px]" level={level} side={side} sideOffset={6}>
          <Icon
            aria-hidden="true"
            className="mt-px shrink-0 text-[var(--color-muted)]"
            size={ICON_SIZE}
            strokeWidth={1.7}
          />
          <span className="min-w-0 whitespace-pre-line leading-snug">{label}</span>
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
