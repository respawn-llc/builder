import { errorMessage } from "../api/errors";
import type { AppServices } from "./services";

type Logger = AppServices["logger"];

export type RecoverableFailureContext = Readonly<Record<string, string>>;

export async function recoverOrThrowDebugFailure({
  context,
  error,
  logger,
  message,
  recover,
}: Readonly<{
  context: RecoverableFailureContext;
  error: unknown;
  logger: Logger;
  message: string;
  recover: () => void;
}>): Promise<void> {
  const diagnostic = { ...context, error: errorMessage(error) };
  await logger.append("warn", message, diagnostic);
  if (kentDebugModeEnabled()) {
    throw new Error(`${message} ${JSON.stringify(diagnostic)}`);
  }
  recover();
}

export function kentDebugModeEnabled(): boolean {
  return debugEnvEnabled(import.meta.env.KENT_DEBUG) || debugQueryEnabled();
}

function debugEnvEnabled(value: unknown): boolean {
  return value === true || value === "true" || value === "1";
}

function debugQueryEnabled(): boolean {
  if (typeof window === "undefined") {
    return false;
  }
  return new URLSearchParams(window.location.search).get("debug") === "true";
}
