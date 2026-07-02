import { load as loadTauriStore } from "@tauri-apps/plugin-store";
import { z } from "zod";

// Desktop-local client settings: device/install-scoped preferences only (never
// server-authoritative state, never synced). Persisted by the native bridge to a
// flat JSON file in the Tauri app data dir (plugin-store BaseDirectory::AppData):
// macOS ~/Library/Application Support/sh.kent/settings.json, Linux
// ~/.local/share/sh.kent/settings.json. See docs/dev/specs/release-distribution.md.

export type DesktopSelfUpdate = "enabled" | "disabled";

export type DesktopSettings = Readonly<{
  // Schema version of the persisted file; bump + extend parseDesktopSettings to
  // migrate when the shape changes.
  version: 1;
  // Whether the in-app self-updater runs. Homebrew installs ship "disabled" (the
  // cask postflight writes it) so brew owns updates and stays in lockstep with the
  // server; direct-download installs leave it "enabled".
  selfUpdate: DesktopSelfUpdate;
}>;

export const desktopSettingsVersion = 1 as const;

export const defaultDesktopSettings: DesktopSettings = {
  version: desktopSettingsVersion,
  selfUpdate: "enabled",
};

const desktopSettingsSchema = z.object({
  selfUpdate: z.unknown().optional(),
});

// parseDesktopSettings validates untyped persisted JSON into typed settings,
// filling defaults for missing/invalid fields and normalizing the schema version.
// It never throws: unknown or corrupt shapes degrade to defaults so a hand-edited
// or partially-written file can't brick startup. Unknown extra fields are ignored,
// so a newer on-disk file stays forward-readable by an older build.
export function parseDesktopSettings(raw: unknown): DesktopSettings {
  const parsed = desktopSettingsSchema.safeParse(raw);
  return {
    version: desktopSettingsVersion,
    selfUpdate: parseSelfUpdate(parsed.success ? parsed.data.selfUpdate : undefined),
  };
}

function parseSelfUpdate(value: unknown): DesktopSelfUpdate {
  return value === "disabled" ? "disabled" : "enabled";
}

// plugin-store file in the Tauri app data dir; fields are stored flat (not
// nested) so the Homebrew cask postflight can write the file as plain JSON.
const desktopSettingsStoreFile = "settings.json";
const desktopSettingsVersionKey = "version";
const desktopSettingsSelfUpdateKey = "selfUpdate";
// Browser QA shell only; the real gate runs on Tauri via plugin-store.
const desktopSettingsBrowserStorageKey = "kent.desktop.settings";

export async function readTauriDesktopSettings(): Promise<DesktopSettings> {
  const store = await loadTauriStore(desktopSettingsStoreFile);
  const [version, selfUpdate] = await Promise.all([
    store.get(desktopSettingsVersionKey),
    store.get(desktopSettingsSelfUpdateKey),
  ]);
  return parseDesktopSettings({ version, selfUpdate });
}

export async function writeTauriDesktopSettings(next: DesktopSettings): Promise<void> {
  const store = await loadTauriStore(desktopSettingsStoreFile);
  await store.set(desktopSettingsVersionKey, next.version);
  await store.set(desktopSettingsSelfUpdateKey, next.selfUpdate);
  await store.save();
}

export function readBrowserDesktopSettings(): DesktopSettings {
  try {
    const raw = globalThis.localStorage.getItem(desktopSettingsBrowserStorageKey);
    return raw === null ? defaultDesktopSettings : parseDesktopSettings(JSON.parse(raw));
  } catch {
    return defaultDesktopSettings;
  }
}

export function writeBrowserDesktopSettings(next: DesktopSettings): void {
  globalThis.localStorage.setItem(desktopSettingsBrowserStorageKey, JSON.stringify(next));
}
