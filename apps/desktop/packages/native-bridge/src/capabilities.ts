export type NativePlatform = "browser" | "linux" | "macos" | "unknown" | "windows";
export type NativeHostPlatform = Exclude<NativePlatform, "browser">;

export type NativeCapabilityState = Readonly<{
  platform: NativePlatform;
  hostPlatform: NativeHostPlatform;
  clipboard: Readonly<{
    writeText: boolean;
    readText: boolean;
  }>;
  directories: Readonly<{
    select: boolean;
  }>;
  files: Readonly<{
    select: boolean;
    open: boolean;
    stat: boolean;
  }>;
  notifications: Readonly<{
    basic: boolean;
  }>;
  links: Readonly<{
    openExternal: boolean;
  }>;
  logging: Readonly<{
    localFile: boolean;
  }>;
  tray: boolean;
  appMenu: boolean;
  updater: boolean;
  settings: boolean;
  windowControls: boolean;
  windowDrag: boolean;
  dialogWindows: boolean;
  projectCreationWindow: boolean;
  macosVibrancy: boolean;
}>;

const unavailableCapabilities: NativeCapabilityState = {
  platform: "browser",
  hostPlatform: "unknown",
  clipboard: {
    writeText: false,
    readText: false,
  },
  directories: {
    select: false,
  },
  files: {
    select: false,
    open: false,
    stat: false,
  },
  notifications: {
    basic: true,
  },
  links: {
    openExternal: false,
  },
  logging: {
    localFile: false,
  },
  tray: false,
  appMenu: false,
  updater: false,
  settings: false,
  windowControls: false,
  windowDrag: false,
  dialogWindows: false,
  projectCreationWindow: false,
  macosVibrancy: false,
};

export function createBrowserCapabilities(
  platform: NativePlatform,
  hostPlatform?: NativeHostPlatform,
): NativeCapabilityState {
  return {
    ...unavailableCapabilities,
    hostPlatform: platform === "browser" ? (hostPlatform ?? detectBrowserHostPlatform()) : platform,
    platform,
    settings: true,
  };
}

export function createTauriCapabilities(platform: NativePlatform): NativeCapabilityState {
  return {
    hostPlatform: nativeHostPlatform(platform),
    platform,
    clipboard: {
      writeText: true,
      readText: true,
    },
    directories: {
      select: true,
    },
    files: {
      select: true,
      open: true,
      stat: true,
    },
    notifications: {
      basic: tauriPlatformSupportsNativeNotifications(platform),
    },
    links: {
      openExternal: true,
    },
    logging: {
      localFile: true,
    },
    tray: false,
    appMenu: false,
    updater: true,
    settings: true,
    windowControls: false,
    windowDrag: true,
    dialogWindows: true,
    projectCreationWindow: true,
    macosVibrancy: false,
  };
}

export function tauriPlatformSupportsNativeNotifications(platform: NativePlatform): boolean {
  return platform === "macos" || platform === "windows" || platform === "linux";
}

export function normalizeNativePlatform(platform: string): NativePlatform {
  if (platform === "linux" || platform === "macos" || platform === "windows") {
    return platform;
  }
  return "unknown";
}

function nativeHostPlatform(platform: NativePlatform): NativeHostPlatform {
  return platform === "browser" ? "unknown" : platform;
}

function detectBrowserHostPlatform(): NativeHostPlatform {
  if (typeof navigator === "undefined") {
    return "unknown";
  }
  if (navigator.platform === "MacIntel") {
    return "macos";
  }
  if (navigator.platform === "Win32") {
    return "windows";
  }
  if (navigator.platform === "Linux x86_64" || navigator.platform === "Linux armv81") {
    return "linux";
  }
  return "unknown";
}
