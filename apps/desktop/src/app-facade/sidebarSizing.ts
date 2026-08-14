export type SidebarResizeBounds = Readonly<{
  maxWidthPx: number;
  minWidthPx: number;
  shellWidthPx: number;
}>;

export type ResolvedSidebarWidth = Readonly<{
  px: number;
}>;

export const sidebarMaxWidthRatio = 0.85;
export const sidebarMinWidthPx = 350;
export const sidebarDismissThresholdPx = 400;
export const sidebarProtectedMainMinWidthPx = 400;
export const sidebarResizeStepPx = 32;

export type SidebarSizePreference = Readonly<{
  desiredWidthPx: number;
  minWidthPx: number;
}>;

export const defaultSidebarSizePreference: SidebarSizePreference = {
  desiredWidthPx: 550,
  minWidthPx: sidebarMinWidthPx,
};

// normalizePx coerces a possibly non-finite (NaN/Infinity) width — e.g. from a
// corrupted cached preference — to a finite integer so it can't propagate into
// an invalid inline width that leaves the sidebar stuck.
function normalizePx(value: number, fallback: number): number {
  return Number.isFinite(value) ? Math.round(value) : fallback;
}

export function clampSidebarWidth(
  widthPx: number,
  maxWidthPx = Number.MAX_SAFE_INTEGER,
  minWidthPx = sidebarMinWidthPx,
): number {
  const roundedMaxWidthPx = Math.max(0, normalizePx(maxWidthPx, Number.MAX_SAFE_INTEGER));
  const effectiveMinWidthPx = Math.min(
    Math.max(0, normalizePx(minWidthPx, sidebarMinWidthPx)),
    roundedMaxWidthPx,
  );
  return Math.min(
    Math.max(normalizePx(widthPx, effectiveMinWidthPx), effectiveMinWidthPx),
    roundedMaxWidthPx,
  );
}

export function initialSidebarWidthForViewport(
  viewportWidthPx: number,
  preference: SidebarSizePreference = defaultSidebarSizePreference,
): number {
  return resolveSidebarWidth(
    preference.desiredWidthPx,
    sidebarResizeBoundsForShellWidth(viewportWidthPx, preference),
  ).px;
}

export function sidebarResizeBoundsForShellWidth(
  shellWidthPx: number,
  preference: SidebarSizePreference = defaultSidebarSizePreference,
): SidebarResizeBounds {
  const roundedShellWidthPx = Math.max(0, normalizePx(shellWidthPx, 0));
  const maxWidthPx = Math.round(roundedShellWidthPx * sidebarMaxWidthRatio);
  return {
    maxWidthPx,
    minWidthPx: Math.min(Math.max(sidebarMinWidthPx, Math.round(preference.minWidthPx)), maxWidthPx),
    shellWidthPx: roundedShellWidthPx,
  };
}

export function sidebarShiftResizeBoundsForCurrentLayout(
  shellWidthPx: number,
  protectedMainWidthPx: number,
  currentSidebarWidthPx: number,
  preference: SidebarSizePreference = defaultSidebarSizePreference,
): SidebarResizeBounds {
  const shellBounds = sidebarResizeBoundsForShellWidth(shellWidthPx, preference);
  const availableWidthPx = Math.max(
    0,
    normalizePx(protectedMainWidthPx, 0) +
      normalizePx(currentSidebarWidthPx, 0) -
      sidebarProtectedMainMinWidthPx,
  );
  const maxWidthPx = Math.min(shellBounds.maxWidthPx, availableWidthPx);
  return {
    maxWidthPx,
    minWidthPx: Math.min(Math.max(sidebarMinWidthPx, Math.round(preference.minWidthPx)), maxWidthPx),
    shellWidthPx: shellBounds.shellWidthPx,
  };
}

export function resolveSidebarWidth(widthPx: number, bounds: SidebarResizeBounds): ResolvedSidebarWidth {
  return { px: clampSidebarWidth(widthPx, bounds.maxWidthPx, bounds.minWidthPx) };
}
