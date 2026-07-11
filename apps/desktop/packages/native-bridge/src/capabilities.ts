export type NativePlatform = "browser" | "linux" | "macos" | "unknown" | "windows";

export type NativeCapabilityState = Readonly<{
  platform: NativePlatform;
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

export function createBrowserCapabilities(platform: NativePlatform): NativeCapabilityState {
  return { ...unavailableCapabilities, platform, settings: true };
}

export function createTauriCapabilities(platform: NativePlatform): NativeCapabilityState {
  return {
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
