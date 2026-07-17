export type AppTheme = "light" | "dark";

export function readEffectiveTheme(): AppTheme {
  if (typeof document !== "undefined") {
    const configured = document.documentElement.getAttribute("data-theme");
    if (configured === "light" || configured === "dark") {
      return configured;
    }
  }
  if (typeof window === "undefined" || !(window.matchMedia instanceof Function)) {
    return "dark";
  }
  return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
}
