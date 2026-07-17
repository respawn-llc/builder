import type { AppServices } from "@/app-facade";

export async function writeClipboardText(
  value: string,
  nativeBridge: AppServices["nativeBridge"],
): Promise<void> {
  if (nativeBridge.capabilities.clipboard.writeText) {
    await nativeBridge.clipboard.writeText(value);
    return;
  }
  await navigator.clipboard.writeText(value);
}
