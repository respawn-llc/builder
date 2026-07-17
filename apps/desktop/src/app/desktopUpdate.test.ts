import {
  createBrowserNativeBridge,
  type NativeBridge,
  type NativeUpdateAvailability,
  type NativeUpdateDownloadProgress,
} from "@app/native-bridge";
import { vi } from "vitest";

import type { AppLogger } from "@/app-facade";
import { checkForDesktopUpdate, installDesktopUpdate } from "./desktopUpdate";

const noUpdate: NativeUpdateAvailability = { available: false };

function availableUpdate(version: string): NativeUpdateAvailability {
  return { available: true, version, currentVersion: "2.0.0", notes: null, publishedAt: null };
}

function fakeLogger(): AppLogger {
  return { append: vi.fn(async () => undefined) };
}

function fakeBridge(
  updates: Partial<NativeBridge["updates"]>,
  options: { updater?: boolean; settings?: Partial<NativeBridge["settings"]> } = {},
): NativeBridge {
  const base = createBrowserNativeBridge();
  return {
    ...base,
    capabilities: { ...base.capabilities, updater: options.updater ?? true },
    updates: { ...base.updates, supported: async () => true, ...updates },
    settings: { ...base.settings, ...options.settings },
  };
}

describe("checkForDesktopUpdate", () => {
  it("skips the check when the shell cannot update", async () => {
    const check = vi.fn(async () => noUpdate);
    const bridge = fakeBridge({ check }, { updater: false });

    await expect(checkForDesktopUpdate(bridge, fakeLogger())).resolves.toEqual({ available: false });
    expect(check).not.toHaveBeenCalled();
  });

  it("skips the check when the platform updater cannot service the install (e.g. a Linux deb)", async () => {
    const check = vi.fn(async () => noUpdate);
    const bridge = fakeBridge({ check, supported: async () => false });

    await expect(checkForDesktopUpdate(bridge, fakeLogger())).resolves.toEqual({ available: false });
    expect(check).not.toHaveBeenCalled();
  });

  it("skips the check when self-update is disabled (e.g. a Homebrew install)", async () => {
    const check = vi.fn(async () => noUpdate);
    const bridge = fakeBridge(
      { check },
      { settings: { read: async () => ({ version: 1, selfUpdate: "disabled" }) } },
    );

    await expect(checkForDesktopUpdate(bridge, fakeLogger())).resolves.toEqual({ available: false });
    expect(check).not.toHaveBeenCalled();
  });

  it("still checks when a settings read fails so updates are not silently suppressed", async () => {
    const bridge = fakeBridge(
      {
        async check() {
          return availableUpdate("2.3.0");
        },
      },
      {
        settings: {
          read: async () => {
            throw new Error("store unavailable");
          },
        },
      },
    );

    await expect(checkForDesktopUpdate(bridge, fakeLogger())).resolves.toEqual({
      available: true,
      version: "2.3.0",
    });
  });

  it("reports an available update with its version", async () => {
    const bridge = fakeBridge({
      async check() {
        return availableUpdate("2.2.0");
      },
    });

    await expect(checkForDesktopUpdate(bridge, fakeLogger())).resolves.toEqual({
      available: true,
      version: "2.2.0",
    });
  });

  it("stays silent and logs when the check fails", async () => {
    const logger = fakeLogger();
    const bridge = fakeBridge({
      async check() {
        throw new Error("network down");
      },
    });

    await expect(checkForDesktopUpdate(bridge, logger)).resolves.toEqual({ available: false });
    expect(logger.append).toHaveBeenCalled();
  });
});

describe("installDesktopUpdate", () => {
  it("downloads, relaunches, and forwards progress", async () => {
    const order: string[] = [];
    const progress: NativeUpdateDownloadProgress[] = [];
    const bridge = fakeBridge({
      async downloadAndInstall(onProgress) {
        order.push("download");
        onProgress?.({ downloadedBytes: 50, totalBytes: 100 });
      },
      async relaunch() {
        order.push("relaunch");
      },
    });

    const result = await installDesktopUpdate(bridge, fakeLogger(), (p) => progress.push(p));

    expect(result).toEqual({ ok: true });
    expect(order).toEqual(["download", "relaunch"]);
    expect(progress).toEqual([{ downloadedBytes: 50, totalBytes: 100 }]);
  });

  it("does not relaunch when the download fails", async () => {
    const logger = fakeLogger();
    const relaunch = vi.fn(async () => undefined);
    const bridge = fakeBridge({
      async downloadAndInstall() {
        throw new Error("download failed");
      },
      relaunch,
    });

    const result = await installDesktopUpdate(bridge, logger, () => undefined);

    expect(result).toEqual({ ok: false });
    expect(relaunch).not.toHaveBeenCalled();
    expect(logger.append).toHaveBeenCalled();
  });

  it("reports failure when the relaunch fails", async () => {
    const logger = fakeLogger();
    const bridge = fakeBridge({
      async relaunch() {
        throw new Error("relaunch failed");
      },
    });

    const result = await installDesktopUpdate(bridge, logger, () => undefined);

    expect(result).toEqual({ ok: false });
    expect(logger.append).toHaveBeenCalled();
  });
});
