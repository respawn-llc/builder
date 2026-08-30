import { TransportError } from "./errors";
import { z } from "zod";
import type { JsonValue } from "./json";
import {
  handleSubscriptionMessage,
  parseFrame,
  sendSocketRequest,
  subscriptionCompleteMethod,
  waitForSubscriptionEnd,
} from "./jsonRpcSocket";
import type { RpcEventHandler } from "./transport";

class TerminalSubscriptionError extends Error {}

export async function runJsonSubscription(
  input: Readonly<{
    socket: WebSocket;
    method: string;
    params: JsonValue;
    handler: RpcEventHandler;
    signal: AbortSignal;
  }>,
): Promise<void> {
  const { socket, method, params, handler, signal } = input;
  let terminal: Readonly<
    | { kind: "complete"; code: number; message: string; reason: string | null }
    | { kind: "error"; error: Error }
  > | null = null;
  let resolveTerminal: (() => void) | null = null;
  const terminalPromise = new Promise<void>((resolve) => {
    resolveTerminal = resolve;
  });
  const completeMethod = subscriptionCompleteMethod(method);
  const currentTerminal = (): typeof terminal => terminal;
  const failTerminal = (error: Error): void => {
    if (terminal !== null) return;
    terminal = { kind: "error", error };
    try {
      handler.onError(error);
    } catch (callbackError) {
      terminal = {
        kind: "error",
        error:
          callbackError instanceof Error
            ? callbackError
            : new TransportError("Subscription error handler failed."),
      };
    } finally {
      resolveTerminal?.();
      socket.close();
    }
  };
  const listener = (event: MessageEvent<unknown>) => {
    if (terminal !== null) return;
    if (isResponseFrame(event.data)) return;
    try {
      const result = handleSubscriptionMessage(event, handler, completeMethod);
      if (result.kind === "complete") {
        terminal = result;
        resolveTerminal?.();
        socket.close();
      }
    } catch (cause) {
      const error = cause instanceof Error ? cause : new TransportError("Subscription message failed.");
      failTerminal(error);
    }
  };
  try {
    socket.addEventListener("message", listener);
    await sendSocketRequest(socket, method, params, { timeoutMilliseconds: 30_000 });
    try {
      handler.onOpen?.();
    } catch (cause) {
      failTerminal(cause instanceof Error ? cause : new TransportError("Subscription open handler failed."));
    }
    await Promise.race([waitForSubscriptionEnd(socket, signal), terminalPromise]);
    throwTerminalResult(method, currentTerminal());
  } catch (error) {
    throwTerminalResult(method, currentTerminal());
    throw error;
  } finally {
    socket.removeEventListener("message", listener);
  }
}

function throwTerminalResult(
  method: string,
  result: Readonly<
    | { kind: "complete"; code: number; message: string; reason: string | null }
    | { kind: "error"; error: Error }
  > | null,
): void {
  if (result === null) return;
  if (result.kind === "error") throw new TerminalSubscriptionError(result.error.message);
  throwNonZero(method, result);
}

function isResponseFrame(data: unknown): boolean {
  const text = z.string().safeParse(data);
  if (!text.success) return false;
  return z
    .object({ jsonrpc: z.literal("2.0"), id: z.string() })
    .loose()
    .safeParse(parseFrame(text.data)).success;
}

export function isTerminalSubscriptionError(error: unknown): boolean {
  return error instanceof TerminalSubscriptionError;
}

function throwNonZero(
  method: string,
  complete: Readonly<{ code: number; message: string; reason: string | null }>,
): void {
  if (complete.code === 0) return;
  const suffix = complete.message.length === 0 ? "" : `: ${complete.message}`;
  throw new TransportError(`${method} subscription completed with code ${complete.code.toString()}${suffix}`);
}
