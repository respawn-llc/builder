import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

import {
  SidebarContext,
  type SidebarCanceledResult,
  type SidebarCancelReason,
  type SidebarDestination,
  type SidebarPhase,
  type SidebarResult,
} from "@/app-facade";
import {
  sidebarSizePreference,
  sidebarWidthProfile,
  sidebarWidthProfileEquals,
  type SidebarWidthProfile,
} from "@/app-facade";
import { initialSidebarWidthForViewport, type ResolvedSidebarWidth } from "@/app-facade";

const sidebarExitAnimationMs = 140;
type SidebarWidthEntry = Readonly<{
  profile: SidebarWidthProfile;
  widthPx: number;
}>;
type SidebarWidths = readonly SidebarWidthEntry[];
const defaultSidebarWidthProfile: SidebarWidthProfile = { kind: "custom", sizing: null };

type PendingSidebar = Readonly<{
  resolve: (result: SidebarResult) => void;
}>;

export function SidebarProvider({ children }: Readonly<{ children: ReactNode }>) {
  const [activeDestination, setActiveDestination] = useState<SidebarDestination | null>(null);
  const [activeWidthProfile, setActiveWidthProfile] =
    useState<SidebarWidthProfile>(defaultSidebarWidthProfile);
  const [phase, setPhase] = useState<SidebarPhase>("open");
  const [sidebarWidths, setSidebarWidths] = useState<SidebarWidths>(() => [
    { profile: defaultSidebarWidthProfile, widthPx: defaultSidebarWidth() },
  ]);
  const sidebarWidthPx =
    sidebarWidthForProfile(sidebarWidths, activeWidthProfile) ?? defaultSidebarWidth(activeDestination);
  const activeDestinationRef = useRef<SidebarDestination | null>(activeDestination);
  const pendingRef = useRef<PendingSidebar | null>(null);
  const closeTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const clearCloseTimeout = useCallback(() => {
    if (closeTimeoutRef.current !== null) {
      clearTimeout(closeTimeoutRef.current);
      closeTimeoutRef.current = null;
    }
  }, []);

  const animateClosed = useCallback(() => {
    clearCloseTimeout();
    setPhase("closing");
    closeTimeoutRef.current = setTimeout(() => {
      closeTimeoutRef.current = null;
      setActiveDestination(null);
      setPhase("open");
    }, sidebarExitAnimationMs);
  }, [clearCloseTimeout]);

  const closeSidebar = useCallback(
    (reason: SidebarCancelReason = "closed") => {
      const pending = pendingRef.current;
      pendingRef.current = null;
      pending?.resolve({ status: "canceled", reason });
      if (activeDestinationRef.current !== null) {
        animateClosed();
      }
    },
    [animateClosed],
  );

  const openSidebar = useCallback(
    async (destination: SidebarDestination): Promise<SidebarResult> => {
      clearCloseTimeout();
      const nextProfile = sidebarWidthProfile(destination);
      setActiveWidthProfile(nextProfile);
      setSidebarWidths((current) => {
        if (sidebarWidthForProfile(current, nextProfile) !== undefined) {
          return current;
        }
        return [...current, { profile: nextProfile, widthPx: defaultSidebarWidth(destination) }];
      });
      const pending = pendingRef.current;
      pendingRef.current = null;
      pending?.resolve({ status: "canceled", reason: "replaced" });
      setPhase("open");
      activeDestinationRef.current = destination;
      setActiveDestination(destination);
      return new Promise<SidebarResult>((resolve) => {
        pendingRef.current = { resolve };
      });
    },
    [clearCloseTimeout],
  );

  const resolveSidebar = useCallback(
    (result: Exclude<SidebarResult, SidebarCanceledResult>) => {
      const pending = pendingRef.current;
      pendingRef.current = null;
      pending?.resolve(result);
      if (activeDestinationRef.current !== null) {
        animateClosed();
      }
    },
    [animateClosed],
  );

  const resizeSidebar = useCallback(
    (width: ResolvedSidebarWidth) => {
      setSidebarWidths((current) => setSidebarWidthForProfile(current, activeWidthProfile, width.px));
    },
    [activeWidthProfile],
  );

  useEffect(() => {
    return clearCloseTimeout;
  }, [clearCloseTimeout]);

  useEffect(() => {
    activeDestinationRef.current = activeDestination;
  }, [activeDestination]);

  const value = useMemo(
    () => ({
      activeDestination,
      closeSidebar,
      openSidebar,
      phase,
      resizeSidebar,
      resolveSidebar,
      sidebarWidthPx,
    }),
    [activeDestination, closeSidebar, openSidebar, phase, resizeSidebar, resolveSidebar, sidebarWidthPx],
  );

  return <SidebarContext.Provider value={value}>{children}</SidebarContext.Provider>;
}

function defaultSidebarWidth(destination: SidebarDestination | null = null): number {
  const sizePreference = sidebarSizePreference(destination);
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
  return widths.map((entry) =>
    sidebarWidthProfileEquals(entry.profile, profile) ? { ...entry, widthPx: resolvedWidthPx } : entry,
  );
}
