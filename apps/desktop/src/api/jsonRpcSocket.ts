import { z } from "zod";

import { ProtocolMismatchError, RpcError, ServerRootMismatchError, TransportError } from "./errors";
import { jsonValueSchema, type JsonValue } from "./json";
import type { RpcEventHandler } from "./transport";

export const protocolVersion = __KENT_PROTOCOL_VERSION__;
export const jsonRpcVersion = "2.0";
export const handshakeMethod = "protocol.handshake";
export const protocolVersionMismatchErrorCode = -32025;

export const responseSchema = z.object({
  jsonrpc: z.literal(jsonRpcVersion),
  id: z.string().optional(),
  result: z.unknown().optional(),
  error: z
    .object({
      code: z.number(),
      message: z.string(),
      data: jsonValueSchema.optional().catch(undefined),
    })
    .optional(),
});

const notificationSchema = z.object({
  jsonrpc: z.literal(jsonRpcVersion),
  method: z.string(),
  params: z.unknown().optional(),
});
const textFrameSchema = z.string();

export async function openSocket(
  endpoint: string,
  timeoutMilliseconds: number,
  signal?: AbortSignal,
): Promise<WebSocket> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted === true) {
      reject(new TransportError(`Connection to ${endpoint} was canceled.`));
      return;
    }
    const socket = new WebSocket(endpoint);
    const timeout = setTimeout(() => {
      fail(new TransportError(`Connection to ${endpoint} timed out.`));
    }, timeoutMilliseconds);
    const cleanup = () => {
      clearTimeout(timeout);
      socket.removeEventListener("open", open);
      socket.removeEventListener("error", error);
      socket.removeEventListener("close", close);
      signal?.removeEventListener("abort", abort);
    };
    const fail = (cause: Error) => {
      cleanup();
      socket.close();
      reject(cause);
    };
    const open = () => {
      cleanup();
      resolve(socket);
    };
    const error = () => {
      fail(new TransportError(`Unable to connect to ${endpoint}.`));
    };
    const close = () => {
      fail(new TransportError(`Connection to ${endpoint} closed before opening.`));
    };
    const abort = () => {
      fail(new TransportError(`Connection to ${endpoint} was canceled.`));
    };
    socket.addEventListener("open", open, { once: true });
    socket.addEventListener("error", error, { once: true });
    socket.addEventListener("close", close, { once: true });
    signal?.addEventListener("abort", abort, { once: true });
  });
}

export function parseFrame(data: string): unknown {
  try {
    return JSON.parse(data);
  } catch {
    return {};
  }
}

const handshakeResultSchema = z.object({
  identity: z
    .object({
      persistence_root_id: z.string().optional(),
    })
    .optional(),
});

// assertHandshakeRoot enforces that a connected server serves the persistence
// root the GUI loaded its configuration from. expectedRootId is empty for the
// default root (validation skipped); otherwise the server's reported
// identity.persistence_root_id must match exactly. A server that reports no id
// (older build) is rejected when an id is required, mirroring the Go client.
export function assertHandshakeRoot(result: unknown, expectedRootId: string): void {
  if (expectedRootId.length === 0) {
    return;
  }
  const parsed = handshakeResultSchema.safeParse(result);
  const reported = parsed.success ? (parsed.data.identity?.persistence_root_id ?? "") : "";
  if (reported !== expectedRootId) {
    throw new ServerRootMismatchError(
      "The Kent server on this endpoint serves a different persistence root than the one this app is configured for. Start a server for the selected root (kent serve --persistence-root <root>) or check KENT_PERSISTENCE_ROOT.",
    );
  }
}

export async function handshakeSubscription(
  socket: WebSocket,
  timeoutMilliseconds: number,
  expectedRootId: string,
  signal?: AbortSignal,
): Promise<void> {
  const options = signal === undefined ? { timeoutMilliseconds } : { timeoutMilliseconds, signal };
  const result = await sendSocketRequest(
    socket,
    handshakeMethod,
    { protocol_version: protocolVersion },
    options,
  );
  assertHandshakeRoot(result, expectedRootId);
}

export async function sendSocketRequest(
  socket: WebSocket,
  method: string,
  params: JsonValue,
  options: Readonly<{
    timeoutMilliseconds: number | null;
    signal?: AbortSignal;
  }>,
): Promise<unknown> {
  const { signal, timeoutMilliseconds } = options;
  const id = `${method}-${Date.now().toString()}`;
  return new Promise((resolve, reject) => {
    if (signal?.aborted === true) {
      reject(new TransportError(`${method} request was canceled.`));
      return;
    }
    const timeout =
      timeoutMilliseconds === null
        ? null
        : setTimeout(() => {
            fail(new TransportError(`${method} request timed out.`));
          }, timeoutMilliseconds);
    const cleanup = () => {
      if (timeout !== null) {
        clearTimeout(timeout);
      }
      socket.removeEventListener("message", listener);
      socket.removeEventListener("close", close);
      socket.removeEventListener("error", error);
      signal?.removeEventListener("abort", abort);
    };
    const fail = (cause: Error) => {
      cleanup();
      reject(cause);
    };
    const listener = (event: MessageEvent<unknown>) => {
      const textFrame = textFrameSchema.safeParse(event.data);
      if (!textFrame.success) {
        return;
      }
      const response = responseSchema.safeParse(parseFrame(textFrame.data));
      if (!response.success || response.data.id !== id) {
        return;
      }
      cleanup();
      if (response.data.error !== undefined) {
        reject(socketRequestError(method, response.data.error));
        return;
      }
      resolve(response.data.result);
    };
    const close = () => {
      fail(new TransportError(`${method} request closed before response.`));
    };
    const error = () => {
      fail(new TransportError(`${method} request failed before response.`));
    };
    const abort = () => {
      fail(new TransportError(`${method} request was canceled.`));
    };
    socket.addEventListener("message", listener);
    socket.addEventListener("close", close, { once: true });
    socket.addEventListener("error", error, { once: true });
    signal?.addEventListener("abort", abort, { once: true });
    try {
      socket.send(JSON.stringify({ jsonrpc: jsonRpcVersion, id, method, params }));
    } catch (cause) {
      fail(cause instanceof Error ? cause : new TransportError(`${method} request failed to send.`));
    }
  });
}

export function socketRequestError(
  method: string,
  error: Readonly<{ code: number; message: string; data?: JsonValue | undefined }>,
): Error {
  if (method === handshakeMethod && error.code === protocolVersionMismatchErrorCode) {
    return new ProtocolMismatchError(error.message);
  }
  return new RpcError({ code: error.code, message: error.message, method, data: error.data });
}

export async function waitForSubscriptionEnd(socket: WebSocket, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted || socket.readyState === WebSocket.CLOSED || socket.readyState === WebSocket.CLOSING) {
      resolve();
      return;
    }
    const cleanup = () => {
      socket.removeEventListener("close", close);
      socket.removeEventListener("error", error);
      signal.removeEventListener("abort", abort);
    };
    const close = () => {
      cleanup();
      if (signal.aborted) {
        resolve();
        return;
      }
      reject(new TransportError("Subscription socket closed."));
    };
    const error = () => {
      cleanup();
      reject(new TransportError("Subscription socket failed."));
    };
    const abort = () => {
      cleanup();
      resolve();
    };
    socket.addEventListener("close", close, { once: true });
    socket.addEventListener("error", error, { once: true });
    signal.addEventListener("abort", abort, { once: true });
  });
}

export async function delay(milliseconds: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const finish = () => {
      clearTimeout(timeout);
      signal.removeEventListener("abort", abort);
      resolve();
    };
    const abort = () => {
      finish();
    };
    const timeout = setTimeout(finish, milliseconds);
    signal.addEventListener("abort", abort, { once: true });
  });
}

export type SubscriptionMessageResult = Readonly<
  { kind: "active" } | { kind: "complete"; code: number; message: string }
>;

export function subscriptionCompleteMethod(subscriptionMethod: string): string | null {
  switch (subscriptionMethod) {
    case "workflow.subscribe":
      return "workflow.complete";
    case "workflow.subscribeProject":
      return "workflow.project.complete";
    case "attention.notification.subscribe":
      return "attention.notification.complete";
    default:
      return null;
  }
}

export function handleSubscriptionMessage(
  event: MessageEvent<unknown>,
  handler: RpcEventHandler,
  completeMethod: string | null,
): SubscriptionMessageResult {
  const textFrame = textFrameSchema.safeParse(event.data);
  if (!textFrame.success) {
    return { kind: "active" };
  }
  const parsed = parseFrame(textFrame.data);
  const notification = notificationSchema.safeParse(parsed);
  if (!notification.success) {
    return { kind: "active" };
  }
  if (completeMethod !== null && notification.data.method === completeMethod) {
    const complete = z
      .object({ code: z.number().optional(), message: z.string().optional() })
      .safeParse(notification.data.params);
    const code = complete.success ? (complete.data.code ?? 0) : 0;
    const message = complete.success ? (complete.data.message ?? "") : "";
    handler.onComplete(code, message);
    return { kind: "complete", code, message };
  }
  handler.onEvent(notification.data.method, notification.data.params);
  return { kind: "active" };
}
