import type { NativeBridge, NativeUpdateDownloadProgress } from "@app/native-bridge";
import { z } from "zod";

import type { GuiLogger } from "./logging";

export type DesktopUpdateAvailability =
  | Readonly<{ available: false }>
  | Readonly<{ available: true; version: string }>;

// Self-update is gated off on Homebrew installs (brew owns updates), on installs the
// platform updater cannot service (Linux deb/plain-binary, where only AppImage
// self-updates), and on shells without the updater capability; transient check
// failures stay silent so the next launch retries instead of surfacing an error chip.
export async function checkForDesktopUpdate(
  nativeBridge: NativeBridge,
  logger: GuiLogger,
): Promise<DesktopUpdateAvailability> {
  if (!nativeBridge.capabilities.updater) {
    return { available: false };
  }
  if (!(await nativeBridge.updates.supported())) {
    return { available: false };
  }
  if (await isSelfUpdateDisabled(nativeBridge, logger)) {
    return { available: false };
  }
  try {
    const result = await nativeBridge.updates.check();
    return result.available ? { available: true, version: result.version } : { available: false };
  } catch (error) {
    await logger.append("warn", "Desktop update check failed.", { error: errorText(error) });
    return { available: false };
  }
}

export type DesktopUpdateInstall = Readonly<{ ok: boolean }>;

// Downloads + installs the pending update (reporting download progress), then
// relaunches into the new version. Relaunch only runs after a clean install.
// Returns ok:false and logs on failure so the caller can surface a retryable
// error instead of leaving the app in a half-updated state.
export async function installDesktopUpdate(
  nativeBridge: NativeBridge,
  logger: GuiLogger,
  onProgress: (progress: NativeUpdateDownloadProgress) => void,
): Promise<DesktopUpdateInstall> {
  try {
    await nativeBridge.updates.downloadAndInstall(onProgress);
  } catch (error) {
    await logger.append("error", "Desktop update download/install failed.", {
      error: errorText(error),
    });
    return { ok: false };
  }
  try {
    await nativeBridge.updates.relaunch();
  } catch (error) {
    await logger.append("error", "Desktop update relaunch failed.", { error: errorText(error) });
    return { ok: false };
  }
  return { ok: true };
}

// Reads the install-local self-update gate. A read failure is treated as "not
// disabled" so a missing/corrupt settings file never silently suppresses updates
// for direct-download installs; the cask explicitly writes "disabled" for brew.
async function isSelfUpdateDisabled(nativeBridge: NativeBridge, logger: GuiLogger): Promise<boolean> {
  try {
    const settings = await nativeBridge.settings.read();
    return settings.selfUpdate === "disabled";
  } catch (error) {
    await logger.append("warn", "Desktop settings read failed.", { error: errorText(error) });
    return false;
  }
}

function errorText(error: unknown): string {
  if (error instanceof Error) {
    return error.message;
  }
  const message = z.string().safeParse(error);
  if (message.success) {
    return message.data;
  }
  return "Unknown desktop update error.";
}
