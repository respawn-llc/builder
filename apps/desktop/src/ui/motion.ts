import { useEffect, useState } from "react";

const motionFastVarName = "--motion-fast";
const fallbackMotionFastMs = 140;

export type OpacityExitPhase = "hidden" | "visible" | "exiting";

export function useOpacityExit(visible: boolean): OpacityExitPhase {
  const [phase, setPhase] = useState<OpacityExitPhase>(() => (visible ? "visible" : "hidden"));
  const [previousVisible, setPreviousVisible] = useState(visible);
  if (previousVisible !== visible) {
    setPreviousVisible(visible);
    setPhase(visible ? "visible" : prefersReducedMotion() ? "hidden" : "exiting");
  }
  useEffect(() => {
    if (phase !== "exiting") {
      return undefined;
    }
    const timer = window.setTimeout(() => {
      setPhase("hidden");
    }, motionDurationFromCSSVar(motionFastVarName, fallbackMotionFastMs));
    return () => {
      window.clearTimeout(timer);
    };
  }, [phase]);
  return phase;
}

export function motionDurationFromCSSVar(name: string, fallbackMs: number): number {
  if (prefersReducedMotion()) {
    return 0;
  }
  const raw = window.getComputedStyle(document.documentElement).getPropertyValue(name);
  return firstDurationMs(raw) ?? fallbackMs;
}

export function prefersReducedMotion(): boolean {
  return window.matchMedia instanceof Function && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

function firstDurationMs(value: string): number | null {
  const token =
    value
      .trim()
      .split(" ")
      .find((part) => part.length > 0) ?? "";
  if (token.endsWith("ms")) {
    const parsed = Number.parseFloat(token.slice(0, -2));
    return Number.isFinite(parsed) ? parsed : null;
  }
  if (token.endsWith("s")) {
    const parsed = Number.parseFloat(token.slice(0, -1));
    return Number.isFinite(parsed) ? parsed * 1000 : null;
  }
  return null;
}
