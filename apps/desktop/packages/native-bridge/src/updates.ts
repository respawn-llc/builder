import { relaunch } from "@tauri-apps/plugin-process";
import { check as checkForUpdate, type Update } from "@tauri-apps/plugin-updater";

export type NativeUpdateAvailability =
  | Readonly<{ available: false }>
  | Readonly<{
      available: true;
      version: string;
      currentVersion: string;
      notes: string | null;
      publishedAt: Date | null;
    }>;

export type NativeUpdateDownloadProgress = Readonly<{
  downloadedBytes: number;
  // Total bytes to download; null when the update server sent no Content-Length.
  totalBytes: number | null;
}>;

export type NativeUpdateBridge = Readonly<{
  // Reports whether the running install can self-update. The Tauri Linux updater
  // only services AppImage bundles, so deb/plain-binary launches return false
  // even though the updater capability is present.
  supported(): Promise<boolean>;
  check(): Promise<NativeUpdateAvailability>;
  downloadAndInstall(onProgress?: (progress: NativeUpdateDownloadProgress) => void): Promise<void>;
  relaunch(): Promise<void>;
}>;

const unavailableUpdate: NativeUpdateAvailability = { available: false };

export function createBrowserUpdates(): NativeUpdateBridge {
  return {
    async supported(): Promise<boolean> {
      return false;
    },
    async check(): Promise<NativeUpdateAvailability> {
      return unavailableUpdate;
    },
    async downloadAndInstall(): Promise<void> {
      throw new Error("Application updates are unavailable in this shell.");
    },
    async relaunch(): Promise<void> {
      throw new Error("Application relaunch is unavailable in this shell.");
    },
  };
}

export function createTauriUpdates(
  selfUpdateSupported: () => Promise<boolean>,
): NativeUpdateBridge {
  let pendingUpdate: Update | null = null;
  return {
    supported: selfUpdateSupported,
    async check(): Promise<NativeUpdateAvailability> {
      const update = await checkForUpdate();
      pendingUpdate = update;
      if (update === null) {
        return unavailableUpdate;
      }
      return {
        available: true,
        version: update.version,
        currentVersion: update.currentVersion,
        notes: update.body ?? null,
        publishedAt: parseUpdateDate(update.date),
      };
    },
    async downloadAndInstall(
      onProgress?: (progress: NativeUpdateDownloadProgress) => void,
    ): Promise<void> {
      if (pendingUpdate === null) {
        throw new Error("No update is pending; call updates.check() first.");
      }
      let downloadedBytes = 0;
      let totalBytes: number | null = null;
      await pendingUpdate.downloadAndInstall((event) => {
        if (event.event === "Started") {
          totalBytes = event.data.contentLength ?? null;
          downloadedBytes = 0;
        } else if (event.event === "Progress") {
          downloadedBytes += event.data.chunkLength;
        }
        onProgress?.({ downloadedBytes, totalBytes });
      });
    },
    async relaunch(): Promise<void> {
      await relaunch();
    },
  };
}

function parseUpdateDate(value: string | undefined): Date | null {
  if (value === undefined) {
    return null;
  }
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
}
