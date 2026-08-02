import { useCallback, type KeyboardEvent as ReactKeyboardEvent, type KeyboardEventHandler } from "react";
import type { NativePlatform } from "@app/native-bridge";
import { useAppServices } from "./useAppServices";
type TextFieldSubmitKeyEvent = Readonly<{
  ctrlKey: boolean; defaultPrevented: boolean; isComposing?: boolean | undefined; key: string; metaKey: boolean;
  nativeEvent?: Readonly<{ isComposing?: boolean | undefined }> | undefined; preventDefault(): void; repeat: boolean;
  stopPropagation(): void;
}>;
type DirectShortcutOptions = Readonly<{ kind: "direct"; action: (() => void) | null; available: boolean }>;
type FormShortcutOptions = Readonly<{ kind: "form"; available: boolean }>;
export function useTextFieldSubmitShortcut(
  options: DirectShortcutOptions,
): KeyboardEventHandler<HTMLElement>;
export function useTextFieldSubmitShortcut(options: FormShortcutOptions): KeyboardEventHandler<HTMLFormElement>;
export function useTextFieldSubmitShortcut(
  options: DirectShortcutOptions | FormShortcutOptions,
): KeyboardEventHandler<HTMLElement | HTMLFormElement> {
  const { nativeBridge } = useAppServices();
  const platform = nativeBridge.capabilities.platform;
  return useCallback(
    (event) => {
      if (options.kind === "direct") {
        handleDirectTextFieldSubmit(event, platform, options.available, options.action);
        return;
      }
      handleFormTextFieldSubmit(event, platform, options.available);
    },
    [options, platform],
  );
}

export function isTextFieldSubmitShortcut(
  event: Pick<TextFieldSubmitKeyEvent, "ctrlKey" | "isComposing" | "key" | "metaKey" | "nativeEvent" | "repeat">,
  platform: NativePlatform,
): boolean {
  if (event.key !== "Enter" || event.isComposing === true || event.nativeEvent?.isComposing === true) {
    return false;
  }
  if (platform === "macos") {
    return event.metaKey;
  }
  if (platform === "linux" || platform === "windows") {
    return event.ctrlKey;
  }
  return false;
}

function handleDirectTextFieldSubmit(
  event: TextFieldSubmitKeyEvent,
  platform: NativePlatform,
  available: boolean,
  action: (() => void) | null,
): void {
  if (!isTextFieldSubmitShortcut(event, platform)) {
    return;
  }
  event.preventDefault();
  event.stopPropagation();
  if (!event.repeat && available) {
    action?.();
  }
}

function handleFormTextFieldSubmit(
  event: ReactKeyboardEvent<HTMLElement | HTMLFormElement>,
  platform: NativePlatform,
  available: boolean,
): void {
  if (event.defaultPrevented || !isTextFieldSubmitShortcut(event, platform)) {
    return;
  }
  const form = event.currentTarget;
  if (!(form instanceof HTMLFormElement)) {
    return;
  }
  const target = event.target;
  if (!isTextualFormTarget(target) || target.form !== form) {
    return;
  }
  event.preventDefault();
  event.stopPropagation();
  if (!event.repeat && available) {
    form.requestSubmit();
  }
}

function isTextualFormTarget(target: EventTarget | null): target is HTMLInputElement | HTMLTextAreaElement {
  if (target instanceof HTMLTextAreaElement) {
    return true;
  }
  if (!(target instanceof HTMLInputElement)) {
    return false;
  }
  return ["email", "password", "search", "tel", "text", "url"].includes(target.type);
}
