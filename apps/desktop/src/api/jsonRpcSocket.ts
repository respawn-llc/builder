import { z } from "zod";

import {
  ConnectionLossError,
  ContractError,
  ProtocolMismatchError,
  RpcError,
  ServerRootMismatchError,
  TransportError,
} from "./errors";
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
const subscriptionCompleteParamsSchema = z
  .object({
    code: z.number().int().optional(),
    message: z.string().optional(),
  })
  .strict();
const textFrameSchema = z.string();
type SubscriptionNotification = z.infer<typeof notificationSchema>;

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
      fail(new ConnectionLossError(`Connection to ${endpoint} timed out.`));
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
      fail(new ConnectionLossError(`Unable to connect to ${endpoint}.`));
    };
    const close = () => {
      fail(new ConnectionLossError(`Connection to ${endpoint} closed before opening.`));
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
      fail(new ConnectionLossError(`${method} request closed before response.`));
    };
    const error = () => {
      fail(new ConnectionLossError(`${method} request failed before response.`));
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
      reject(new ConnectionLossError("Subscription socket closed."));
    };
    const error = () => {
      cleanup();
      reject(new ConnectionLossError("Subscription socket failed."));
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
  | { kind: "active" }
  | { kind: "complete"; code: number; message: string }
  | { kind: "terminal_failure"; error: Error }
>;

type DecodedSubscriptionFrame = Readonly<
  | { kind: "notification"; notification: SubscriptionNotification }
  | { kind: "response" }
  | { kind: "terminal_failure"; error: Error }
>;

export function subscriptionCompleteMethod(subscriptionMethod: string): string | null {
  switch (subscriptionMethod) {
    case "workflow.subscribe":
      return "workflow.complete";
    case "workflow.subscribeProject":
      return "workflow.project.complete";
    case "attention.notification.subscribe":
      return "attention.notification.complete";
    case "worktree.setup.subscribe":
      return "worktree.setup.complete";
    default:
      return null;
  }
}

export function handleSubscriptionMessage(
  event: MessageEvent<unknown>,
  handler: RpcEventHandler,
  completeMethod: string | null,
): SubscriptionMessageResult {
  const decoded = decodeSubscriptionFrame(event);
  if (decoded.kind !== "notification") {
    return decoded.kind === "response" ? { kind: "active" } : decoded;
  }
  if (completeMethod !== null && decoded.notification.method === completeMethod) {
    return handleSubscriptionCompletion(decoded.notification.params, handler);
  }
  return handleSubscriptionEvent(decoded.notification, handler);
}

function decodeSubscriptionFrame(event: MessageEvent<unknown>): DecodedSubscriptionFrame {
  const textFrame = textFrameSchema.safeParse(event.data);
  if (!textFrame.success) {
    return {
      kind: "terminal_failure",
      error: new ContractError("Subscription received an unsupported WebSocket frame."),
    };
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(textFrame.data);
  } catch {
    return {
      kind: "terminal_failure",
      error: new ContractError("Subscription received malformed JSON."),
    };
  }
  const notification = notificationSchema.safeParse(parsed);
  if (notification.success) {
    return { kind: "notification", notification: notification.data };
  }
  if (responseSchema.safeParse(parsed).success) {
    return { kind: "response" };
  }
  return {
    kind: "terminal_failure",
    error: new ContractError("Subscription received a frame outside the JSON-RPC contract."),
  };
}

function handleSubscriptionCompletion(
  params: unknown,
  handler: RpcEventHandler,
): SubscriptionMessageResult {
  const complete = subscriptionCompleteParamsSchema.safeParse(params ?? {});
  if (!complete.success) {
    return {
      kind: "terminal_failure",
      error: new ContractError("Subscription completion did not match the JSON-RPC contract."),
    };
  }
  const code = complete.data.code ?? 0;
  const message = complete.data.message ?? "";
  try {
    handler.onComplete(code, message);
  } catch (error) {
    return {
      kind: "terminal_failure",
      error: error instanceof Error ? error : new ContractError("Subscription completion handler failed."),
    };
  }
  return { kind: "complete", code, message };
}

function handleSubscriptionEvent(
  notification: SubscriptionNotification,
  handler: RpcEventHandler,
): SubscriptionMessageResult {
  try {
    const handlerError = handler.onEvent(notification.method, notification.params);
    if (handlerError instanceof Error) {
      return { kind: "terminal_failure", error: handlerError };
    }
  } catch (error) {
    return {
      kind: "terminal_failure",
      error: error instanceof Error ? error : new ContractError("Subscription event handler failed."),
    };
  }
  return { kind: "active" };
}
