import { useCallback, type KeyboardEvent as ReactKeyboardEvent, type KeyboardEventHandler } from "react";
import type { NativePlatform } from "@app/native-bridge";

import {
  consumeTextFieldSubmitShortcut,
  isTextFieldSubmitShortcut,
  type TextFieldSubmitShortcutPolicy,
} from "@/ui";
import { useAppServices } from "./useAppServices";

type DirectShortcutOptions = Readonly<{ kind: "direct"; action: (() => void) | null; available: boolean }>;
type FormShortcutOptions = Readonly<{ kind: "form"; available: boolean }>;

export function useTextFieldSubmitShortcut(options: DirectShortcutOptions): KeyboardEventHandler<HTMLElement>;
export function useTextFieldSubmitShortcut(
  options: FormShortcutOptions,
): KeyboardEventHandler<HTMLFormElement>;
export function useTextFieldSubmitShortcut(
  options: DirectShortcutOptions | FormShortcutOptions,
): KeyboardEventHandler<HTMLElement | HTMLFormElement> {
  const policy = useTextFieldSubmitShortcutPolicy();
  return useCallback(
    (event) => {
      if (options.kind === "direct") {
        handleDirectTextFieldSubmit(event, policy, options.available, options.action);
        return;
      }
      handleFormTextFieldSubmit(event, policy, options.available);
    },
    [options, policy],
  );
}

export function useTextFieldSubmitShortcutPolicy(): TextFieldSubmitShortcutPolicy {
  const { nativeBridge } = useAppServices();
  return textFieldSubmitShortcutPolicyForPlatform(nativeBridge.capabilities.platform);
}

export function textFieldSubmitShortcutPolicyForPlatform(
  platform: NativePlatform,
): TextFieldSubmitShortcutPolicy {
  if (platform === "macos") {
    return "meta-enter";
  }
  if (platform === "linux" || platform === "windows") {
    return "control-enter";
  }
  return "unavailable";
}

export { isTextFieldSubmitShortcut };
export type { TextFieldSubmitShortcutPolicy };

function handleDirectTextFieldSubmit(
  event: ReactKeyboardEvent<HTMLElement>,
  policy: TextFieldSubmitShortcutPolicy,
  available: boolean,
  action: (() => void) | null,
): void {
  if (!consumeTextFieldSubmitShortcut(event, policy)) {
    return;
  }
  if (!event.repeat && available) {
    action?.();
  }
}

function handleFormTextFieldSubmit(
  event: ReactKeyboardEvent<HTMLElement | HTMLFormElement>,
  policy: TextFieldSubmitShortcutPolicy,
  available: boolean,
): void {
  if (event.defaultPrevented || !isTextFieldSubmitShortcut(event, policy)) {
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
  if (!consumeTextFieldSubmitShortcut(event, policy)) {
    return;
  }
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
