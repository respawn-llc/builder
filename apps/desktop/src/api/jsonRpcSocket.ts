import { z } from "zod";

import {
  classifyResult,
  create,
  OperationOutcome,
  operationFromDescriptor,
  type DescMethod,
  type Message,
  type MessageShape,
} from "@app/server-api-contract";
import { ConnectionService } from "@app/server-api-contract/gen/kent/api/connection/connection_pb";

import {
  binaryFrameBytes,
  binaryFramePayload,
  completeDescriptorResponse,
  decodeDescriptorResponse,
  encodeDescriptorCall,
} from "./descriptorRpc";
import { ProtocolMismatchError, RpcError, ServerRootMismatchError, TransportError } from "./errors";
import { jsonValueSchema, type JsonValue } from "./json";
import { projectRpcErrorFromClassifiedFailure } from "./projectRpcError";
import type { RpcEventHandler } from "./transport";

export const protocolVersion = __KENT_PROTOCOL_VERSION__;
export const jsonRpcVersion = "2.0";

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
    socket.binaryType = "arraybuffer";
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

export async function sendSocketDescriptorRequest<Method extends DescMethod>(
  socket: WebSocket,
  method: Method,
  request: MessageShape<Method["input"]>,
  options: Readonly<{
    timeoutMilliseconds: number | null;
    signal?: AbortSignal;
  }>,
): Promise<MessageShape<Method["output"]>> {
  const { signal, timeoutMilliseconds } = options;
  const correlation = `${method.name}-${Date.now().toString()}`;
  const { operation, bytes } = encodeDescriptorCall(method, request, correlation);
  return new Promise((resolve, reject) => {
    if (signal?.aborted === true) {
      reject(new TransportError(`${operation.name} request was canceled.`));
      return;
    }
    const timeout =
      timeoutMilliseconds === null
        ? null
        : setTimeout(() => {
            fail(new TransportError(`${operation.name} request timed out.`));
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
      const frame = binaryFrameBytes(event.data);
      if (frame === undefined) {
        return;
      }
      try {
        const response = decodeDescriptorResponse(frame);
        if (response.correlation !== correlation) {
          return;
        }
        cleanup();
        resolve(completeDescriptorResponse(method, correlation, response));
      } catch (cause) {
        fail(
          cause instanceof Error ? cause : new TransportError(`${operation.name} response decoding failed.`),
        );
      }
    };
    const close = () => {
      fail(new TransportError(`${operation.name} request closed before response.`));
    };
    const error = () => {
      fail(new TransportError(`${operation.name} request failed before response.`));
    };
    const abort = () => {
      fail(new TransportError(`${operation.name} request was canceled.`));
    };
    socket.addEventListener("message", listener);
    socket.addEventListener("close", close, { once: true });
    socket.addEventListener("error", error, { once: true });
    signal?.addEventListener("abort", abort, { once: true });
    try {
      socket.send(binaryFramePayload(bytes));
    } catch (cause) {
      fail(cause instanceof Error ? cause : new TransportError(`${operation.name} request failed to send.`));
    }
  });
}

export function parseFrame(data: string): unknown {
  try {
    return JSON.parse(data);
  } catch {
    return {};
  }
}

export async function setupSocket(
  socket: WebSocket,
  options: Readonly<{
    timeoutMilliseconds: number;
    expectedRootId: string;
    signal?: AbortSignal;
    sessionID?: string;
  }>,
): Promise<void> {
  const requestOptions =
    options.signal === undefined
      ? { timeoutMilliseconds: options.timeoutMilliseconds }
      : { timeoutMilliseconds: options.timeoutMilliseconds, signal: options.signal };
  const handshake = await sendSocketDescriptorRequest(
    socket,
    ConnectionService.method.handshake,
    create(ConnectionService.method.handshake.input, { protocolVersion }),
    requestOptions,
  );
  requireDescriptorSuccess(ConnectionService.method.handshake, handshake);
  const identity = handshake.outcome.case === "success" ? handshake.outcome.value.identity : undefined;
  assertReportedRoot(identity?.persistenceRootId, options.expectedRootId);
  if (options.sessionID !== undefined) {
    const attachment = await sendSocketDescriptorRequest(
      socket,
      ConnectionService.method.attachSession,
      create(ConnectionService.method.attachSession.input, { sessionId: options.sessionID }),
      requestOptions,
    );
    requireDescriptorSuccess(ConnectionService.method.attachSession, attachment);
  }
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
  return new RpcError({ code: error.code, message: error.message, method, data: error.data });
}

function requireDescriptorSuccess(method: DescMethod, result: Message): void {
  const classified = classifyResult(method.output, result);
  if (classified.outcome === OperationOutcome.SUCCESS) {
    return;
  }
  const operation = operationFromDescriptor(method);
  if (
    method === ConnectionService.method.handshake &&
    classified.failure.code === "protocol_version_mismatch"
  ) {
    throw new ProtocolMismatchError("unsupported protocol version");
  }
  if (
    method === ConnectionService.method.attachProject ||
    method === ConnectionService.method.attachSession
  ) {
    throw projectRpcErrorFromClassifiedFailure(operation.name, classified.failure);
  }
  throw new TransportError(`${operation.name} failed with code ${classified.failure.code}.`);
}

function assertReportedRoot(reported: string | undefined, expectedRootId: string): void {
  if (expectedRootId.length === 0) {
    return;
  }
  if ((reported ?? "") !== expectedRootId) {
    throw new ServerRootMismatchError(
      "The Kent server on this endpoint serves a different persistence root than the one this app is configured for. Start a server for the selected root (kent serve --persistence-root <root>) or check KENT_PERSISTENCE_ROOT.",
    );
  }
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
