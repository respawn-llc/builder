import defaultCapability from "../../src-tauri/capabilities/default.json";
import tauriConfig from "../../src-tauri/tauri.conf.json";

export const allowedNativeConfigValues = [defaultCapability, tauriConfig] satisfies readonly unknown[];
