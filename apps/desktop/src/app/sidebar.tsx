import { ArrowLeft, X } from "lucide-react";
import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type PointerEvent,
} from "react";
import { useTranslation } from "react-i18next";

import { cx, IconTooltipButton } from "@/ui";
import { SidebarHeaderActionProvider, SidebarHeaderActionSlot } from "@/app-facade";
import { SidebarDestinationView } from "./sidebarDestinations";
import { SidebarHeaderOffsetContext } from "@/app-facade";
import { sidebarTitle } from "@/app-facade";
import { sidebarSizePreference } from "@/app-facade";
import { useSidebarShell } from "@/app-facade";
import { useSidebarCurrentPage } from "./sidebarPageContext";
import {
  sidebarMaxWidthRatio,
  sidebarMinWidthPx,
  sidebarResizeBoundsForShellWidth,
  sidebarResizeStepPx,
  resolveSidebarWidth,
  type SidebarResizeBounds,
} from "@/app-facade";

export function SidebarHost() {
  const { t } = useTranslation();
  const currentPage = useSidebarCurrentPage();
  const {
    activeDestination,
    back,
    backAvailable,
    canGoBack,
    close,
    closeAvailable,
    phase,
    resize,
    sidebarWidthPx,
    transitionDirection,
  } = useSidebarShell();
  const sizePreference = useMemo(() => sidebarSizePreference(activeDestination), [activeDestination]);
  const titleId = useId();
  const sidebarRef = useRef<HTMLElement | null>(null);
  const headerRef = useRef<HTMLElement | null>(null);
  const [headerOffsetPx, setHeaderOffsetPx] = useState(0);
  const resizeDragRef = useRef<SidebarResizeDrag | null>(null);
  const [resizing, setResizing] = useState(false);
  const [resizeBounds, setResizeBounds] = useState(() =>
    sidebarResizeBoundsForShellWidth(fallbackSidebarShellWidth(), sizePreference),
  );

  const sidebarStyle = useMemo<SidebarStyle>(
    () => ({
      "--app-sidebar-header-height": `${headerOffsetPx.toString()}px`,
      "--app-sidebar-inset": "var(--space-2)",
      "--app-sidebar-width": `${sidebarWidthPx.toString()}px`,
    }),
    [headerOffsetPx, sidebarWidthPx],
  );

  const resizeTo = useCallback(
    (widthPx: number) => {
      const nextBounds = sidebarResizeBounds(sidebarRef.current, sizePreference);
      setResizeBounds(nextBounds);
      resize(resolveSidebarWidth(widthPx, nextBounds));
    },
    [resize, sizePreference],
  );

  const startResize = useCallback(
    (event: PointerEvent<HTMLDivElement>) => {
      if (event.button !== 0) {
        return;
      }
      event.preventDefault();
      const nextBounds = sidebarResizeBounds(sidebarRef.current, sizePreference);
      setResizeBounds(nextBounds);
      resizeDragRef.current = {
        bounds: nextBounds,
        pointerID: event.pointerId,
        startWidth: sidebarWidthPx,
        startX: event.clientX,
      };
      setPointerCaptureIfAvailable(event.currentTarget, event.pointerId);
      setResizing(true);
    },
    [sidebarWidthPx, sizePreference],
  );

  const resizeFromPointer = useCallback(
    (event: PointerEvent<HTMLDivElement>) => {
      const drag = resizeDragRef.current;
      if (drag?.pointerID !== event.pointerId) {
        return;
      }
      event.preventDefault();
      resize(resolveSidebarWidth(drag.startWidth + drag.startX - event.clientX, drag.bounds));
    },
    [resize],
  );

  const stopResize = useCallback((event: PointerEvent<HTMLDivElement>) => {
    const drag = resizeDragRef.current;
    if (drag?.pointerID !== event.pointerId) {
      return;
    }
    resizeDragRef.current = null;
    releasePointerCaptureIfAvailable(event.currentTarget, event.pointerId);
    setResizing(false);
  }, []);

  const resizeWithKeyboard = useCallback(
    (event: KeyboardEvent<HTMLDivElement>) => {
      if (event.key === "ArrowLeft") {
        event.preventDefault();
        resizeTo(sidebarWidthPx + sidebarResizeStepPx);
        return;
      }
      if (event.key === "ArrowRight") {
        event.preventDefault();
        resizeTo(sidebarWidthPx - sidebarResizeStepPx);
        return;
      }
      if (event.key === "Home") {
        event.preventDefault();
        resizeTo(sidebarMinWidthPx);
        return;
      }
      if (event.key === "End") {
        event.preventDefault();
        resizeTo(resizeBounds.maxWidthPx);
      }
    },
    [resizeBounds.maxWidthPx, resizeTo, sidebarWidthPx],
  );

  useEffect(() => {
    if (!resizing) {
      return;
    }
    const previousCursor = document.body.style.cursor;
    const previousUserSelect = document.body.style.userSelect;
    document.body.style.cursor = "ew-resize";
    document.body.style.userSelect = "none";
    return () => {
      document.body.style.cursor = previousCursor;
      document.body.style.userSelect = previousUserSelect;
    };
  }, [resizing]);

  useLayoutEffect(() => {
    const header = headerRef.current;
    if (header === null) {
      return;
    }
    const measure = () => {
      setHeaderOffsetPx(header.getBoundingClientRect().height);
    };
    measure();
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    observer?.observe(header);
    return () => {
      observer?.disconnect();
    };
  }, [activeDestination]);

  useEffect(() => {
    if (activeDestination === null) {
      return;
    }
    const clampToCurrentBounds = () => {
      const nextBounds = sidebarResizeBounds(sidebarRef.current, sizePreference);
      setResizeBounds(nextBounds);
      resize(resolveSidebarWidth(sidebarWidthPx, nextBounds));
    };
    clampToCurrentBounds();
    const shellElement = sidebarRef.current?.closest('[data-testid="app-shell-content"]') ?? null;
    const resizeObserver =
      typeof ResizeObserver === "undefined" || shellElement === null
        ? null
        : new ResizeObserver(clampToCurrentBounds);
    if (resizeObserver !== null && shellElement !== null) {
      resizeObserver.observe(shellElement);
    }
    window.addEventListener("resize", clampToCurrentBounds);
    return () => {
      resizeObserver?.disconnect();
      window.removeEventListener("resize", clampToCurrentBounds);
    };
  }, [activeDestination, resize, sidebarWidthPx, sizePreference]);

  if (activeDestination === null) {
    return null;
  }
  const openPage = requireCurrentPage(currentPage);

  const title = sidebarTitle(activeDestination, t);
  const mode = activeDestination.mode ?? "shift";
  const PageBoundary = openPage.Boundary;

  return (
    <SidebarHeaderActionProvider>
      <aside
        aria-labelledby={titleId}
        className={cx(
          "app-region-no-drag app-sidebar-panel island-glass z-10 overflow-hidden",
          "w-[var(--app-sidebar-width)] min-w-[var(--app-sidebar-width)] rounded-[var(--radius-xl)]",
          mode === "shift" &&
            "app-sidebar-panel-shift relative mr-[var(--app-sidebar-inset)] mt-[var(--app-sidebar-inset)] h-[calc(100%-(var(--app-sidebar-inset)*2))] shrink-0 self-start",
          mode === "overlay" &&
            "app-sidebar-panel-overlay fixed top-[calc(var(--native-titlebar-height)+var(--app-sidebar-inset))] right-[var(--app-sidebar-inset)] bottom-[var(--app-sidebar-inset)]",
          phase === "closing" && "app-sidebar-panel-closing",
        )}
        data-testid="app-sidebar-host"
        data-mode={mode}
        data-state={phase}
        ref={sidebarRef}
        role="complementary"
        style={sidebarStyle}
      >
        <div
          aria-label={t("app.resizeSidebar")}
          aria-orientation="vertical"
          aria-valuemax={resizeBounds.maxWidthPx}
          aria-valuemin={resizeBounds.minWidthPx}
          aria-valuenow={sidebarWidthPx}
          className={cx(
            "absolute top-0 bottom-0 left-0 z-20 w-3 cursor-ew-resize touch-none",
            "after:absolute after:top-[var(--space-4)] after:bottom-[var(--space-4)] after:left-1/2 after:w-px after:-translate-x-1/2 after:rounded-full after:bg-transparent after:transition-colors",
            "hover:after:bg-[var(--color-primary)] focus-visible:outline-none focus-visible:after:bg-[var(--color-primary)]",
            resizing && "after:bg-[var(--color-primary)]",
          )}
          data-testid="app-sidebar-resize-handle"
          onKeyDown={resizeWithKeyboard}
          onPointerCancel={stopResize}
          onPointerDown={startResize}
          onPointerMove={resizeFromPointer}
          onPointerUp={stopResize}
          role="separator"
          tabIndex={0}
        />
        {/* The title column is content-sized so a long node/edge id ellipsizes inside the flexible
            action track instead of forcing the title to truncate. The action track is floored at
            min-content (the fixed-size buttons), so a pathologically long title truncates the title
            rather than pushing the inbox/pop-out/delete controls off the header. */}
        <header
          className="absolute top-0 right-0 left-0 z-10 grid grid-cols-[auto_minmax(0,auto)_minmax(min-content,1fr)] items-center gap-[var(--space-3)] border-b border-[var(--color-outline)] bg-[var(--color-island-0)] px-[var(--space-4)] py-[var(--space-3)] [backdrop-filter:blur(8px)]"
          ref={headerRef}
        >
          <div className="flex items-center gap-[var(--space-2)]" data-testid="app-sidebar-leading-controls">
            <IconTooltipButton
              disabled={!closeAvailable}
              label={t("app.close")}
              onClick={close}
            >
              <X aria-hidden="true" size={18} strokeWidth={1.5} />
            </IconTooltipButton>
            {canGoBack ? (
              <IconTooltipButton
                disabled={!backAvailable}
                label={t("app.back")}
                onClick={back}
              >
                <ArrowLeft aria-hidden="true" size={18} strokeWidth={1.5} />
              </IconTooltipButton>
            ) : null}
          </div>
          <h2 className="m-0 min-w-0 truncate text-[1.05rem] font-bold" id={titleId}>
            {title}
          </h2>
          <div className="flex min-w-0 items-center justify-end gap-[var(--space-2)] justify-self-end">
            <SidebarHeaderActionSlot />
          </div>
        </header>
        <div
          className={cx(
            "absolute right-0 bottom-0 left-0 min-h-0",
            activeDestination.kind === "workflowEditor"
              ? "top-[var(--app-sidebar-header-height)] overflow-hidden p-[var(--space-2)]"
              : activeDestination.kind === "taskDetail" || activeDestination.kind === "projectEdit"
                ? "top-0 overflow-hidden"
                : "top-0 overflow-y-auto px-[var(--space-4)] pb-[var(--space-4)] pt-[calc(var(--app-sidebar-header-height)+var(--space-4))]",
          )}
        >
          <PageBoundary>
            <div
              className="app-sidebar-page h-full"
              data-direction={transitionDirection ?? undefined}
              data-testid="app-sidebar-page"
            >
              <SidebarHeaderOffsetContext.Provider value={headerOffsetPx}>
                <SidebarDestinationView
                  destination={activeDestination}
                  navigator={openPage.navigator}
                  retainedState={openPage.retainedState}
                />
              </SidebarHeaderOffsetContext.Provider>
            </div>
          </PageBoundary>
        </div>
      </aside>
    </SidebarHeaderActionProvider>
  );
}

function requireCurrentPage(page: ReturnType<typeof useSidebarCurrentPage>) {
  if (page === null) {
    throw new Error("An open sidebar requires a current page.");
  }
  return page;
}

type SidebarStyle = CSSProperties &
  Readonly<Record<"--app-sidebar-header-height" | "--app-sidebar-inset" | "--app-sidebar-width", string>>;

type PointerCaptureTarget = Partial<
  Readonly<{
    releasePointerCapture(pointerID: number): void;
    setPointerCapture(pointerID: number): void;
  }>
>;

type SidebarResizeDrag = Readonly<{
  bounds: SidebarResizeBounds;
  pointerID: number;
  startWidth: number;
  startX: number;
}>;

function sidebarResizeBounds(
  sidebarElement: HTMLElement | null,
  sizePreference: ReturnType<typeof sidebarSizePreference>,
): SidebarResizeBounds {
  const shellWidth = sidebarElement
    ?.closest('[data-testid="app-shell-content"]')
    ?.getBoundingClientRect().width;
  if (shellWidth === undefined || shellWidth === 0) {
    return sidebarResizeBoundsForShellWidth(fallbackSidebarShellWidth(), sizePreference);
  }
  return sidebarResizeBoundsForShellWidth(shellWidth, sizePreference);
}

function fallbackSidebarShellWidth(): number {
  if (typeof window === "undefined") {
    return Math.ceil(sidebarMinWidthPx / sidebarMaxWidthRatio);
  }
  return window.innerWidth;
}

function setPointerCaptureIfAvailable(element: PointerCaptureTarget, pointerID: number): void {
  element.setPointerCapture?.(pointerID);
}

function releasePointerCaptureIfAvailable(element: PointerCaptureTarget, pointerID: number): void {
  element.releasePointerCapture?.(pointerID);
}
