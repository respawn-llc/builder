import { useId, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { Maximize2, Minus } from "lucide-react";

import { cx } from "./classes";
import { islandSurfaceClassName, type IslandLevel } from "./islandSurfaceStyles";
import { motionDurationFromCSSVar } from "./motion";

export type FloatingNoticeTone = "danger" | "neutral";

export type FloatingNoticeIslandProps = Readonly<{
  children: ReactNode;
  className?: string | undefined;
  collapsed: boolean;
  collapseLabel: string;
  expandedClassName?: string | undefined;
  expandLabel: string;
  icon?: ReactNode;
  onCollapsedChange: (collapsed: boolean) => void;
  positionClassName?: string | undefined;
  positionStrategy?: "absolute" | "fixed" | undefined;
  level?: IslandLevel | undefined;
  title: string;
  tone?: FloatingNoticeTone;
}>;

type FloatingNoticePhase = "collapsed" | "collapsing" | "expanding" | "expanded";

const motionMorphVarName = "--motion-morph";
const motionFastVarName = "--motion-fast";
const fallbackMotionMorphMs = 350;
const fallbackMotionFastMs = 140;

export function FloatingNoticeIsland({
  children,
  className,
  collapsed,
  collapseLabel,
  expandedClassName,
  expandLabel,
  icon,
  level = 1,
  onCollapsedChange,
  positionClassName = "right-[var(--space-4)] bottom-[var(--space-4)]",
  positionStrategy = "fixed",
  title,
  tone = "danger",
}: FloatingNoticeIslandProps) {
  const titleID = useId();
  const [phase, setPhase] = useState<FloatingNoticePhase>(() => (collapsed ? "collapsed" : "expanded"));
  const previousCollapsed = useRef(collapsed);
  useLayoutEffect(() => {
    if (previousCollapsed.current === collapsed) {
      return undefined;
    }
    previousCollapsed.current = collapsed;
    const motionDelay = collapsed
      ? motionDurationFromCSSVar(motionFastVarName, fallbackMotionFastMs)
      : motionDurationFromCSSVar(motionMorphVarName, fallbackMotionMorphMs);
    setPhase(collapsed ? "collapsing" : "expanding");
    const timer = window.setTimeout(() => {
      setPhase(collapsed ? "collapsed" : "expanded");
    }, motionDelay);
    return () => {
      window.clearTimeout(timer);
    };
  }, [collapsed]);
  const styles = noticeToneStyles[tone];
  const expandedClasses =
    expandedClassName ??
    "floating-notice-expanded min-h-[123px] max-h-[min(400px,calc(100vh-32px))] w-[min(420px,calc(100vw-32px))] rounded-[var(--radius-xl)] p-[var(--space-3)]";
  const shellCollapsed = phase === "collapsed";
  const contentVisible = phase === "expanded";
  const collapsedButtonVisible = phase === "collapsed";
  const shellClassName = floatingNoticeShellClassName({
    className,
    collapsed: shellCollapsed,
    expandedClasses,
    level,
    positionClassName,
    positionStrategy,
    styles,
  });

  return (
    <aside
      aria-label={shellCollapsed ? title : undefined}
      aria-labelledby={shellCollapsed ? undefined : titleID}
      className={shellClassName}
      data-state={phase}
      data-testid="floating-notice-shell"
    >
      <div className="min-h-0 overflow-hidden">
        <div
          aria-hidden={!contentVisible}
          className={cx(
            "floating-notice-content grid max-h-full min-h-0 min-w-0 content-start gap-[var(--space-3)] overflow-y-auto overflow-x-hidden",
            contentVisible ? "pointer-events-auto opacity-100" : "pointer-events-none opacity-0",
          )}
          data-testid="floating-notice-content"
          inert={!contentVisible}
        >
          <header
            className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-[var(--space-2)] leading-none"
            data-testid="floating-notice-header"
          >
            <h2 className={cx("m-0 text-lg font-bold leading-none", styles.titleClassName)} id={titleID}>
              {title}
            </h2>
            <button
              aria-label={collapseLabel}
              className="grid h-[18px] w-[18px] place-items-center rounded-full border border-transparent bg-transparent text-[var(--color-on-island)]"
              onClick={() => {
                onCollapsedChange(true);
              }}
              type="button"
            >
              <Minus aria-hidden="true" size={18} strokeWidth={1.5} />
            </button>
          </header>
          {children}
        </div>
      </div>
      <button
        aria-label={expandLabel}
        className={cx(
          "floating-notice-collapsed-button absolute inset-0 grid place-items-center rounded-[var(--radius-m)] border border-transparent bg-transparent",
          collapsedButtonVisible ? "pointer-events-auto opacity-100" : "pointer-events-none opacity-0",
          styles.collapsedTextClassName,
        )}
        data-testid="floating-notice-collapsed-button"
        inert={!collapsedButtonVisible}
        onClick={() => {
          onCollapsedChange(false);
        }}
        type="button"
      >
        {icon ?? <Maximize2 aria-hidden="true" size={24} strokeWidth={1.7} />}
      </button>
    </aside>
  );
}

function floatingNoticeShellClassName({
  className,
  collapsed,
  expandedClasses,
  level,
  positionClassName,
  positionStrategy,
  styles,
}: Readonly<{
  className: string | undefined;
  collapsed: boolean;
  expandedClasses: string;
  level: IslandLevel;
  positionClassName: string;
  positionStrategy: "absolute" | "fixed";
  styles: (typeof noticeToneStyles)[FloatingNoticeTone];
}>): string {
  const sizeClassName = collapsed
    ? cx(
        "floating-notice-collapsed grid-rows-[0fr] h-12 w-12 rounded-[var(--radius-m)] p-0",
        styles.collapsedTextClassName,
      )
    : cx("grid-rows-[1fr]", expandedClasses);
  return cx(
    "floating-notice-morph app-region-no-drag z-50 grid overflow-hidden",
    positionStrategy,
    islandSurfaceClassName(level),
    sizeClassName,
    positionClassName,
    styles.borderClassName,
    collapsed ? styles.collapsedClassName : undefined,
    "overflow-hidden",
    className,
  );
}

const noticeToneStyles: Record<
  FloatingNoticeTone,
  Readonly<{
    borderClassName: string;
    collapsedClassName: string;
    collapsedTextClassName: string;
    titleClassName: string;
  }>
> = {
  danger: {
    borderClassName: "border-[var(--color-error)]",
    collapsedClassName: "floating-notice-collapsed-danger",
    collapsedTextClassName: "text-[var(--color-on-error)]",
    titleClassName: "text-[var(--color-error)]",
  },
  neutral: {
    borderClassName: "border-[var(--color-outline)]",
    collapsedClassName: "floating-notice-collapsed-neutral",
    collapsedTextClassName: "text-[var(--color-on-island)]",
    titleClassName: "text-[var(--color-on-island)]",
  },
};
