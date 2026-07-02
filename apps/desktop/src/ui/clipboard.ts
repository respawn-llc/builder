import type { NativeBridge } from "@app/native-bridge";

export async function writeClipboardText(value: string, nativeBridge: NativeBridge): Promise<void> {
  if (nativeBridge.capabilities.clipboard.writeText) {
    await nativeBridge.clipboard.writeText(value);
    return;
  }
  await navigator.clipboard.writeText(value);
}
