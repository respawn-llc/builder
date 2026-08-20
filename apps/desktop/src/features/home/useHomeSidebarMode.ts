import { useLayoutEffect, useRef, useState, type RefObject } from "react";

import {
  sidebarProtectedMainMinWidthPx,
  taskDetailSidebarSizePreference,
  type SidebarMode,
} from "@/app-facade";

const homeShiftSidebarInsetPx = 8;

export function useHomeSidebarMode(): Readonly<{
  mainPaneRef: RefObject<HTMLElement | null>;
  sidebarMode: SidebarMode;
}> {
  const mainPaneRef = useRef<HTMLElement | null>(null);
  const [availableWidthPx, setAvailableWidthPx] = useState<number | null>(null);
  useLayoutEffect(() => {
    const mainPane = mainPaneRef.current;
    if (mainPane === null) {
      return;
    }
    const measure = () => {
      setAvailableWidthPx(mainPane.getBoundingClientRect().width);
    };
    measure();
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    observer?.observe(mainPane);
    return () => {
      observer?.disconnect();
    };
  }, []);
  const shiftRequiredWidthPx =
    sidebarProtectedMainMinWidthPx + taskDetailSidebarSizePreference.desiredWidthPx + homeShiftSidebarInsetPx;
  return {
    mainPaneRef,
    sidebarMode: availableWidthPx !== null && availableWidthPx >= shiftRequiredWidthPx ? "shift" : "overlay",
  };
}
