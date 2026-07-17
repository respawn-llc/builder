const relativeFormatter = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

export function formatRelativeTime(unixMs: number, nowMs = Date.now()): string {
  if (unixMs <= 0) {
    return "";
  }
  const deltaSeconds = Math.round((unixMs - nowMs) / 1000);
  const minutes = Math.round(deltaSeconds / 60);
  const hours = Math.round(minutes / 60);
  const days = Math.round(hours / 24);
  if (Math.abs(days) >= 1) {
    return relativeFormatter.format(days, "day");
  }
  if (Math.abs(hours) >= 1) {
    return relativeFormatter.format(hours, "hour");
  }
  if (Math.abs(minutes) >= 1) {
    return relativeFormatter.format(minutes, "minute");
  }
  return relativeFormatter.format(deltaSeconds, "second");
}

export function basename(path: string): string {
  const normalized = path.trim().replaceAll("\\", "/");
  const parts = normalized.split("/").filter((part) => part.length > 0);
  return parts.at(-1) ?? normalized;
}

export type PathPlatform = "linux" | "macos" | "windows" | "browser" | "unknown";

export function formatHomeRelativePath(path: string, homePath: string, platform: PathPlatform): string {
  const trimmedPath = path.trim();
  const trimmedHomePath = homePath.trim();
  if (trimmedPath.length === 0 || trimmedHomePath.length === 0) {
    return path;
  }
  const normalizedPath = normalizePathForComparison(trimmedPath);
  const normalizedHomePath = normalizePathForComparison(trimmedHomePath);
  const comparisonPath = comparablePath(normalizedPath, platform);
  const comparisonHomePath = comparablePath(normalizedHomePath, platform);
  if (comparisonPath === comparisonHomePath) {
    return "~";
  }
  if (!comparisonPath.startsWith(`${comparisonHomePath}/`)) {
    return path;
  }
  const relativePath = normalizedPath.slice(normalizedHomePath.length + 1);
  const separator = platform === "windows" ? "\\" : "/";
  return `~${separator}${relativePath.split("/").join(separator)}`;
}

export function projectKeyFromName(name: string): string {
  let letters = "";
  for (const char of name.toUpperCase()) {
    if (isProjectKeyChar(char)) {
      letters += char;
    }
  }
  const candidate = letters.length >= 2 ? letters : `${letters}PR`;
  return candidate.slice(0, 8);
}

function isProjectKeyChar(char: string): boolean {
  const code = char.charCodeAt(0);
  return (code >= 65 && code <= 90) || (code >= 48 && code <= 57);
}

function normalizePathForComparison(path: string): string {
  let normalized = path.replaceAll("\\", "/");
  while (normalized.length > 1 && normalized.endsWith("/")) {
    normalized = normalized.slice(0, -1);
  }
  return normalized;
}

function comparablePath(path: string, platform: PathPlatform): string {
  return platform === "windows" ? path.toLowerCase() : path;
}
