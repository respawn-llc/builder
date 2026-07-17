import { invoke } from "@tauri-apps/api/core";
import { Store } from "@tauri-apps/plugin-store";

export const allowedTauriValues = [Store, invoke] satisfies readonly unknown[];
