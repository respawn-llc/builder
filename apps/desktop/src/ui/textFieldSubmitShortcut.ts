export type TextFieldSubmitShortcutPolicy = "control-enter" | "meta-enter" | "unavailable";

type TextFieldSubmitKeyEvent = Readonly<{
  altKey: boolean;
  ctrlKey: boolean;
  isComposing?: boolean | undefined;
  key: string;
  metaKey: boolean;
  nativeEvent?: Readonly<{ isComposing?: boolean | undefined }> | undefined;
  repeat: boolean;
  shiftKey: boolean;
  preventDefault(): void;
  stopPropagation(): void;
}>;

export function isTextFieldSubmitShortcut(
  event: Pick<
    TextFieldSubmitKeyEvent,
    "altKey" | "ctrlKey" | "isComposing" | "key" | "metaKey" | "nativeEvent" | "repeat" | "shiftKey"
  >,
  policy: TextFieldSubmitShortcutPolicy,
): boolean {
  if (
    policy === "unavailable" ||
    event.key !== "Enter" ||
    event.isComposing === true ||
    event.nativeEvent?.isComposing === true ||
    event.altKey ||
    event.shiftKey
  ) {
    return false;
  }
  if (policy === "meta-enter") {
    return event.metaKey && !event.ctrlKey;
  }
  return event.ctrlKey && !event.metaKey;
}

export function consumeTextFieldSubmitShortcut(
  event: TextFieldSubmitKeyEvent,
  policy: TextFieldSubmitShortcutPolicy,
): boolean {
  if (!isTextFieldSubmitShortcut(event, policy)) {
    return false;
  }
  event.preventDefault();
  event.stopPropagation();
  return true;
}
