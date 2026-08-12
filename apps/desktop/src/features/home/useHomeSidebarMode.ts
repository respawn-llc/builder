import { useSyncExternalStore } from "react";

import type { SidebarMode } from "@/app-facade";

const homeShiftSidebarQuery = "(min-width: 1001px)";

export function useHomeSidebarMode(): SidebarMode {
  const shifted = useSyncExternalStore(subscribeHomeSidebarMode, homeSidebarShifted, () => false);
  return shifted ? "shift" : "overlay";
}

function homeSidebarMediaQuery(): MediaQueryList | null {
  if (!(globalThis.matchMedia instanceof Function)) {
    return null;
  }
  return globalThis.matchMedia(homeShiftSidebarQuery);
}

function subscribeHomeSidebarMode(onChange: () => void): () => void {
  const query = homeSidebarMediaQuery();
  if (query === null) {
    return unavailableMediaQuerySubscription;
  }
  query.addEventListener("change", onChange);
  return () => {
    query.removeEventListener("change", onChange);
  };
}

function homeSidebarShifted(): boolean {
  return homeSidebarMediaQuery()?.matches ?? false;
}

function unavailableMediaQuerySubscription(): void {
  return;
}
