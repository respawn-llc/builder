import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";

import {
  SidebarRootContext,
  SidebarShellContext,
  initialSidebarWidthForViewport,
  sidebarSizePreference,
  sidebarWidthProfile,
  sidebarWidthProfileEquals,
  type ResolvedSidebarWidth,
  type SidebarDestination,
  type SidebarDestinationPolicy,
  type SidebarNavigationOutcome,
  type SidebarWidthProfile,
} from "@/app-facade";
import { createSidebarStack, emptySidebarStackView, type SidebarStackView } from "./sidebarStack";
import { SidebarCurrentPageContext, type SidebarCurrentPage } from "./sidebarPageContext";

const defaultSidebarWidthProfile: SidebarWidthProfile = { kind: "custom", sizing: null };
type SidebarWidthEntry = Readonly<{ profile: SidebarWidthProfile; widthPx: number }>;
type SidebarWidths = readonly SidebarWidthEntry[];
export function SidebarProvider({
  children,
  policy,
}: Readonly<{ children: ReactNode; policy: SidebarDestinationPolicy }>) {
  const [view, setView] = useState<SidebarStackView>(emptySidebarStackView);
  const [sidebarWidths, setSidebarWidths] = useState<SidebarWidths>(() => [
    { profile: defaultSidebarWidthProfile, widthPx: defaultSidebarWidth() },
  ]);
  const publish = useCallback((next: SidebarStackView) => {
    const destination = next.entries.at(-1)?.destination;
    if (destination !== undefined) {
      const profile = sidebarWidthProfile(destination);
      setSidebarWidths((current) =>
        sidebarWidthForProfile(current, profile) === undefined
          ? [...current, { profile, widthPx: defaultSidebarWidth(destination) }]
          : current,
      );
    }
    setView(next);
  }, []);
  const [stack] = useState(() => createSidebarStack(policy, publish));
  const current = view.entries.at(-1);
  const activeWidthProfile = useMemo(
    () => (current === undefined ? defaultSidebarWidthProfile : sidebarWidthProfile(current.destination)),
    [current],
  );
  const resize = useCallback(
    (width: ResolvedSidebarWidth) => {
      setSidebarWidths((current) => setSidebarWidthForProfile(current, activeWidthProfile, width.px));
    },
    [activeWidthProfile],
  );

  useEffect(() => stack.dispose, [stack]);

  const availability = current?.capability.availability;
  const rootValue = useMemo(() => ({ open: stack.open }), [stack]);
  const shellValue = useMemo(
    () => ({
      activeDestination: current?.destination ?? null,
      back: (): SidebarNavigationOutcome =>
        current === undefined || view.entries.length === 1 || availability?.back === false
          ? "unavailable"
          : current.navigator.back(),
      backAvailable: availability?.back ?? true,
      canGoBack: view.entries.length > 1,
      close: (): SidebarNavigationOutcome =>
        current === undefined || availability?.close === false ? "unavailable" : current.navigator.close(),
      closeAvailable: availability?.close ?? true,
      phase: view.phase,
      resize,
      sidebarWidthPx:
        sidebarWidthForProfile(sidebarWidths, activeWidthProfile) ??
        defaultSidebarWidth(current?.destination),
      transitionDirection: view.transitionDirection,
    }),
    [activeWidthProfile, availability, current, resize, sidebarWidths, view],
  );
  const pageValue = useMemo<SidebarCurrentPage | null>(() => {
    if (current === undefined) {
      return null;
    }
    return current.retainedState === undefined
      ? {
          Boundary: current.Boundary,
          destination: current.destination,
          navigator: current.navigator,
        }
      : {
          Boundary: current.Boundary,
          destination: current.destination,
          navigator: current.navigator,
          retainedState: current.retainedState,
        };
  }, [current]);

  return (
    <SidebarRootContext.Provider value={rootValue}>
      <SidebarShellContext.Provider value={shellValue}>
        <SidebarCurrentPageContext.Provider value={pageValue}>{children}</SidebarCurrentPageContext.Provider>
      </SidebarShellContext.Provider>
    </SidebarRootContext.Provider>
  );
}

function defaultSidebarWidth(destination?: SidebarDestination): number {
  const sizePreference = sidebarSizePreference(destination ?? null);
  if (typeof window === "undefined") {
    return initialSidebarWidthForViewport(0, sizePreference);
  }
  return initialSidebarWidthForViewport(window.innerWidth, sizePreference);
}

function sidebarWidthForProfile(widths: SidebarWidths, profile: SidebarWidthProfile): number | undefined {
  return widths.find((entry) => sidebarWidthProfileEquals(entry.profile, profile))?.widthPx;
}

function setSidebarWidthForProfile(
  widths: SidebarWidths,
  profile: SidebarWidthProfile,
  widthPx: number,
): SidebarWidths {
  const resolvedWidthPx = Math.max(0, Math.round(widthPx));
  if (sidebarWidthForProfile(widths, profile) === undefined) {
    return [...widths, { profile, widthPx: resolvedWidthPx }];
  }
  if (sidebarWidthForProfile(widths, profile) === resolvedWidthPx) {
    return widths;
  }
  return widths.map((entry) =>
    sidebarWidthProfileEquals(entry.profile, profile) ? { ...entry, widthPx: resolvedWidthPx } : entry,
  );
}
