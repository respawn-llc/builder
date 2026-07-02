import { invoke } from "@tauri-apps/api/core";

export type NativeDirectoryPickerOptions = Readonly<{
  title: string;
}>;

export type NativeDirectorySelection = Readonly<{
  path: string;
}> | null;

export type NativeFilePickerOptions = Readonly<{
  title: string;
}>;

export type NativeFileSelection = Readonly<{
  path: string;
}> | null;

export type NativeFileTarget = Readonly<{
  path: string;
  basePath?: string | undefined;
}>;

export type NativeDirectoryBridge = Readonly<{
  selectDirectory(options: NativeDirectoryPickerOptions): Promise<NativeDirectorySelection>;
}>;

export type NativeFileBridge = Readonly<{
  selectFile(options: NativeFilePickerOptions): Promise<NativeFileSelection>;
  fileAvailable(target: NativeFileTarget): Promise<boolean>;
  openFile(target: NativeFileTarget): Promise<void>;
}>;

export function createBrowserDirectoryBridge(): NativeDirectoryBridge {
  return {
    async selectDirectory(): Promise<NativeDirectorySelection> {
      throw new Error("Directory selection is unavailable in this shell.");
    },
  };
}

export function createBrowserFileBridge(): NativeFileBridge {
  return {
    async selectFile(): Promise<NativeFileSelection> {
      throw new Error("File selection is unavailable in this shell.");
    },
    async fileAvailable(): Promise<boolean> {
      return false;
    },
    async openFile(): Promise<void> {
      throw new Error("File opening is unavailable in this shell.");
    },
  };
}

export function createTauriDirectoryBridge(): NativeDirectoryBridge {
  return {
    async selectDirectory(options: NativeDirectoryPickerOptions): Promise<NativeDirectorySelection> {
      const path = await invoke<string | null>("select_directory", { title: options.title });
      return path === null ? null : { path };
    },
  };
}

export function createTauriFileBridge(): NativeFileBridge {
  return {
    async selectFile(options: NativeFilePickerOptions): Promise<NativeFileSelection> {
      const path = await invoke<string | null>("select_file", { title: options.title });
      return path === null ? null : { path };
    },
    async fileAvailable(target: NativeFileTarget): Promise<boolean> {
      return invoke<boolean>("file_available", {
        basePath: target.basePath ?? "",
        path: target.path,
      });
    },
    async openFile(target: NativeFileTarget): Promise<void> {
      await invoke("open_file_path", {
        basePath: target.basePath ?? "",
        path: target.path,
      });
    },
  };
}
