import type { NativeLogEntry } from "@app/native-bridge";

export type AppLogLevel = NativeLogEntry["level"];

export type AppLogger = Readonly<{
  append(level: AppLogLevel, message: string, context?: Readonly<Record<string, string>>): Promise<void>;
}>;
