import type { NativePlatform } from "@app/native-bridge";
import type { CSSProperties } from "react";

export type AppChromeTopTreatment = Readonly<{
  classNames: readonly string[];
  effect: "blur" | "fade";
  style: CSSProperties;
}>;

const appChromeContrastFadeClassNames = [
    "pointer-events-none",
    "fixed",
    "inset-x-0",
    "top-0",
    "z-10",
    "h-[calc(var(--native-titlebar-height)*1.5)]",
] as const;

const appChromeContrastFadeStyle = {
    background:
        "linear-gradient(to bottom, color-mix(in srgb, var(--background) 50%, transparent) 0%, 66%, transparent 100%)",
} satisfies CSSProperties;

const appChromeProgressiveBlurClassNames = [
    "pointer-events-none",
    "fixed",
    "inset-x-0",
    "top-0",
    "z-10",
    "h-[calc(var(--native-titlebar-height)*2)]",
] as const;

const appChromeProgressiveBlurStyle = {
    WebkitBackdropFilter: "blur(16px) saturate(0.8) brightness(0.78)",
    WebkitMaskImage: "linear-gradient(to bottom, black 0%, black 30%, transparent 100%)",
    background: "color-mix(in srgb, var(--window-glass-tint) 65%, transparent)",
    backdropFilter: "blur(16px) saturate(0.8) brightness(0.78)",
    maskImage: "linear-gradient(to bottom, black 0%, black 30%, transparent 100%)",
} satisfies CSSProperties;

const appChromeContrastFadeTreatment: AppChromeTopTreatment = {
    classNames: appChromeContrastFadeClassNames,
    effect: "fade",
    style: appChromeContrastFadeStyle,
};

const appChromeProgressiveBlurTreatment: AppChromeTopTreatment = {
    classNames: appChromeProgressiveBlurClassNames,
    effect: "blur",
    style: appChromeProgressiveBlurStyle,
};

export function appChromeTopTreatmentForPlatform(platform: NativePlatform): AppChromeTopTreatment {
    return platform === "windows" ? appChromeProgressiveBlurTreatment : appChromeContrastFadeTreatment;
}

export const appChromeTitleClassNames = [
    "pointer-events-none",
    "fixed",
    "top-[8px]",
    "z-30",
    "h-6",
    "max-w-[min(520px,45vw)]",
    "truncate",
    "text-[12pt]",
    "font-medium",
    "leading-6",
    "text-[var(--color-on-island)]",
] as const;

export const appChromeInlineTitleClassNames = [
    "pointer-events-none",
    "ml-[var(--space-2)]",
    "h-6",
    "max-w-[min(520px,45vw)]",
    "truncate",
    "text-[12pt]",
    "font-medium",
    "leading-6",
    "text-left",
    "text-[var(--color-on-island)]",
] as const;

export function appChromeTitlePlacementClassNames(macOS: boolean): readonly string[] {
    return macOS ? ["right-[var(--space-2)]", "text-right"] : ["left-[var(--space-2)]", "text-left"];
}
