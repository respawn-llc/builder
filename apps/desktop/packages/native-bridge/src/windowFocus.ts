import { getCurrentWindow, type Window as TauriWindow } from "@tauri-apps/api/window";

export type NativeWindowFocusControls = Readonly<{
  isFocused(): Promise<boolean>;
  focusMain(): Promise<void>;
  onFocusChanged(handler: (focused: boolean) => void): Promise<() => void>;
}>;

export function createBrowserWindowFocusControls(): NativeWindowFocusControls {
  return {
    async isFocused(): Promise<boolean> {
      return typeof document !== "undefined" && document.hasFocus();
    },
    async focusMain(): Promise<void> {
      if (typeof window !== "undefined") {
        window.focus();
      }
    },
    async onFocusChanged(handler: (focused: boolean) => void): Promise<() => void> {
      if (typeof window === "undefined") {
        return () => undefined;
      }
      const notifyFocused = (): void => {
        handler(true);
      };
      const notifyBlurred = (): void => {
        handler(false);
      };
      window.addEventListener("focus", notifyFocused);
      window.addEventListener("blur", notifyBlurred);
      return () => {
        window.removeEventListener("focus", notifyFocused);
        window.removeEventListener("blur", notifyBlurred);
      };
    },
  };
}

export function createTauriWindowFocusControls(
  getWindow: () => TauriWindow = getCurrentWindow,
): NativeWindowFocusControls {
  return {
    async isFocused(): Promise<boolean> {
      return getWindow().isFocused();
    },
    async focusMain(): Promise<void> {
      const window = getWindow();
      await window.show();
      await window.setFocus();
    },
    async onFocusChanged(handler: (focused: boolean) => void): Promise<() => void> {
      return getWindow().onFocusChanged((event) => {
        handler(event.payload);
      });
    },
  };
}
