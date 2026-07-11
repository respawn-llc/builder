import { useEffect } from "react";
import type { NativeBridge, NativeWindowGlassTint } from "@app/native-bridge";

import { errorMessage } from "../api/errors";

const themeAttribute = "data-theme";
const windowGlassFillClassName = "window-glass-fill";

export function useNativeWindowGlassTintSync(nativeBridge: NativeBridge): void {
  useEffect(() => {
    const platform = nativeBridge.capabilities.platform;
    if ((platform !== "macos" && platform !== "windows") || typeof document === "undefined") {
      return;
    }

    const syncTint = () => {
      const tint = readNativeWindowGlassTint();
      void nativeBridge.window.setCurrentGlassTint(tint).catch((error: unknown) => {
        logNativeGlassTintSyncError(nativeBridge, tint, error);
      });
    };
    syncTint();

    const observer = new MutationObserver(syncTint);
    observer.observe(document.documentElement, {
      attributeFilter: [themeAttribute],
      attributes: true,
    });

    const systemTheme =
      window.matchMedia instanceof Function ? window.matchMedia("(prefers-color-scheme: light)") : null;
    systemTheme?.addEventListener("change", syncTint);
    return () => {
      observer.disconnect();
      systemTheme?.removeEventListener("change", syncTint);
    };
  }, [nativeBridge]);
}

export function readNativeWindowGlassTint(): NativeWindowGlassTint | null {
  if (typeof document === "undefined") {
    return null;
  }

  const probe = document.createElement("div");
  probe.className = windowGlassFillClassName;
  probe.style.position = "fixed";
  probe.style.inset = "0";
  probe.style.pointerEvents = "none";
  probe.style.visibility = "hidden";
  document.body.append(probe);
  try {
    const backgroundColor = getComputedStyle(probe).backgroundColor;
    return parseCssColorToNativeTint(backgroundColor);
  } finally {
    probe.remove();
  }
}

function logNativeGlassTintSyncError(
  nativeBridge: NativeBridge,
  tint: NativeWindowGlassTint | null,
  error: unknown,
): void {
  void nativeBridge.logging
    .append({
      context: { error: errorMessage(error), tint: JSON.stringify(tint) },
      level: "warn",
      message: "Native window glass tint sync failed.",
      occurredAt: new Date().toISOString(),
    })
    .catch(() => undefined);
}

export function parseCssColorToNativeTint(value: string): NativeWindowGlassTint | null {
  const normalized = value.trim().toLowerCase();
  if (normalized.startsWith("rgba(")) {
    return parseRgbFunction(normalized.slice("rgba(".length, -1));
  }
  if (normalized.startsWith("rgb(")) {
    return parseRgbFunction(normalized.slice("rgb(".length, -1));
  }
  return null;
}

function parseRgbFunction(content: string): NativeWindowGlassTint | null {
  const [rawChannels, rawAlpha] = splitAlpha(content);
  const channels = rawChannels.includes(",")
    ? rawChannels.split(",").map((part) => part.trim())
    : rawChannels
        .split(" ")
        .map((part) => part.trim())
        .filter((part) => part.length > 0);
  if (channels.length !== 3) {
    return null;
  }

  const [rawRed, rawGreen, rawBlue] = channels;
  if (rawRed === undefined || rawGreen === undefined || rawBlue === undefined) {
    return null;
  }
  const red = parseColorChannel(rawRed);
  const green = parseColorChannel(rawGreen);
  const blue = parseColorChannel(rawBlue);
  const alpha = rawAlpha === null ? 1 : parseAlphaChannel(rawAlpha.trim());
  if (red === null || green === null || blue === null || alpha === null) {
    return null;
  }
  return { alpha, blue, green, red };
}

function splitAlpha(content: string): readonly [string, string | null] {
  if (content.includes("/")) {
    const [channels, alpha] = content.split("/", 2);
    if (channels === undefined || alpha === undefined) {
      return [content, null];
    }
    return [channels.trim(), alpha.trim()];
  }
  if (content.includes(",")) {
    const parts = content.split(",");
    if (parts.length === 4) {
      return [parts.slice(0, 3).join(","), parts[3] ?? null];
    }
  }
  return [content, null];
}

function parseColorChannel(value: string): number | null {
  if (value.endsWith("%")) {
    return parseUnitInterval(value.slice(0, -1), 100);
  }
  return parseUnitInterval(value, 255);
}

function parseAlphaChannel(value: string): number | null {
  if (value.endsWith("%")) {
    return parseUnitInterval(value.slice(0, -1), 100);
  }
  return parseUnitInterval(value, 1);
}

function parseUnitInterval(raw: string, divisor: number): number | null {
  const parsed = Number.parseFloat(raw);
  if (!Number.isFinite(parsed)) {
    return null;
  }
  return Math.min(1, Math.max(0, parsed / divisor));
}
