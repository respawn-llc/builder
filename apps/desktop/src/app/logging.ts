import type { NativeBridge } from "@app/native-bridge";

import type { AppLogger, AppLogLevel } from "@/app-facade";

const maxLogBytes = 10 * 1024 * 1024;
const redactedValue = "[redacted]";
const sensitiveKeys = new Set(["authorization", "token", "api_key", "apikey", "password", "secret"]);

export type GuiLogEntry = Readonly<{
  level: AppLogLevel;
  message: string;
  context: Readonly<Record<string, string>>;
  occurredAt: string;
}>;

export type GuiLogger = AppLogger &
  Readonly<{
    entries(): readonly GuiLogEntry[];
  }>;

export function createGuiLogger(nativeBridge: NativeBridge): GuiLogger {
  const entries: GuiLogEntry[] = [];
  let byteSize = 0;

  async function append(
    level: AppLogLevel,
    message: string,
    context: Readonly<Record<string, string>> = {},
  ): Promise<void> {
    const entry: GuiLogEntry = {
      level,
      message,
      context: redactContext(context),
      occurredAt: new Date().toISOString(),
    };
    const encodedSize = new TextEncoder().encode(JSON.stringify(entry)).byteLength + 1;
    entries.push(entry);
    byteSize += encodedSize;
    while (byteSize > maxLogBytes && entries.length > 0) {
      const removed = entries.shift();
      if (removed !== undefined) {
        byteSize -= new TextEncoder().encode(JSON.stringify(removed)).byteLength + 1;
      }
    }
    try {
      await nativeBridge.logging.append(entry);
    } catch {
      return;
    }
  }

  return {
    entries() {
      return entries.slice();
    },
    append,
  };
}

function redactContext(context: Readonly<Record<string, string>>): Readonly<Record<string, string>> {
  return Object.fromEntries(
    Object.entries(context).map(([key, value]) => [
      key,
      sensitiveKeys.has(key.toLowerCase()) ? redactedValue : value,
    ]),
  );
}
