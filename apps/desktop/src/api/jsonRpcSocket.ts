import { z } from "zod";

import {
  create,
  decode,
  decodeEnvelope,
  operationName,
  subscriptionAssociations,
  type DescMessage,
  type DescMethod,
  type MessageShape,
} from "@app/server-api-contract";
import { ConnectionService } from "@app/server-api-contract/gen/kent/api/connection/connection_pb";

import {
  binaryFrameBytes,
  binaryFramePayload,
  completeDescriptorResponse,
  decodeDescriptorResponse,
  encodeDescriptorCall,
  encodeDescriptorSubscriptionCall,
} from "./descriptorRpc";
import { ProtocolMismatchError, RpcError, ServerRootMismatchError, TransportError } from "./errors";
import { jsonValueSchema, type JsonValue } from "./json";
import { protobufRpcError } from "./protobufRpc";
import type {
  DescriptorSubscriptionContract,
  DescriptorSubscriptionHandler,
  DescriptorTerminalOutcome,
  RpcEventHandler,
} from "./transport";

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
type SocketResponse<Result> = Readonly<{ kind: "unmatched" }> | Readonly<{ kind: "matched"; result: Result }>;
type SocketRequestOptions = Readonly<{
  timeoutMilliseconds: number | null;
  signal?: AbortSignal;
}>;

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
  options: SocketRequestOptions,
): Promise<MessageShape<Method["output"]>> {
  const correlation = `${method.name}-${Date.now().toString()}`;
  const { operation, bytes } = encodeDescriptorCall(method, request, correlation);
  return sendSocketFrame(
    socket,
    { label: operation, frame: binaryFramePayload(bytes) },
    (event): SocketResponse<MessageShape<Method["output"]>> => {
      const frame = binaryFrameBytes(event.data);
      if (frame === undefined) {
        return { kind: "unmatched" };
      }
      const response = decodeDescriptorResponse(frame);
      if (response.correlation !== correlation) {
        return { kind: "unmatched" };
      }
      return { kind: "matched", result: completeDescriptorResponse(method, correlation, response) };
    },
    options,
  );
}

export async function runSocketDescriptorSubscription<
  Method extends DescMethod,
  EventDescriptor extends DescMessage,
  CompletionDescriptor extends DescMessage,
  Event,
>(
  input: Readonly<{
    socket: WebSocket;
    method: Method;
    request: MessageShape<Method["input"]>;
    contract: DescriptorSubscriptionContract<Method, EventDescriptor, CompletionDescriptor, Event>;
    handler: DescriptorSubscriptionHandler<Event>;
    signal: AbortSignal;
  }>,
): Promise<void> {
  const { socket, method, request, contract, handler, signal } = input;
  const associations = subscriptionAssociations(method);
  requireAssociatedDescriptor(associations.event.input, contract.eventDescriptor, "event");
  requireAssociatedDescriptor(associations.completion.input, contract.completionDescriptor, "completion");
  const correlation = `${method.name}-${Date.now().toString()}`;
  const { operation, bytes } = encodeDescriptorSubscriptionCall(method, request, correlation);
  return new Promise((resolve, reject) => {
    let acknowledged = false;
    let terminal: DescriptorTerminalOutcome | null = null;
    let settled = false;
    const cleanup = () => {
      socket.removeEventListener("message", message);
      socket.removeEventListener("close", close);
      socket.removeEventListener("error", error);
      signal.removeEventListener("abort", abort);
    };
    const finish = (failure?: Error) => {
      if (settled) return;
      settled = true;
      cleanup();
      if (failure === undefined) resolve();
      else reject(failure);
    };
    const terminalError = (outcome: Extract<DescriptorTerminalOutcome, { kind: "error" }>) =>
      new TransportError(
        `${operation} subscription completed with code ${outcome.code.toString()}: ${outcome.diagnostic}`,
      );
    const settleTerminal = () => {
      if (terminal === null) return;
      finish(terminal.kind === "normal" ? undefined : terminalError(terminal));
    };
    const handleResponse = (frame: Uint8Array) => {
      const response = decodeDescriptorResponse(frame);
      if (response.correlation !== correlation) return;
      contract.projectStart(completeDescriptorResponse(method, correlation, response));
      acknowledged = true;
      if (terminal === null) handler.onOpen?.();
      else settleTerminal();
    };
    const handleNotification = (
      notification: Readonly<{ operation: string; payload?: Uint8Array | undefined }>,
    ) => {
      if (notification.payload === undefined) {
        throw new TransportError(`${notification.operation} notification payload is required.`);
      }
      if (notification.operation === operationName(associations.event)) {
        handler.onEvent(contract.projectEvent(decode(contract.eventDescriptor, notification.payload)));
        return;
      }
      if (notification.operation === operationName(associations.completion)) {
        terminal = contract.classifyCompletion(decode(contract.completionDescriptor, notification.payload));
        handler.onTerminal(terminal);
        socket.close();
        if (acknowledged) settleTerminal();
        return;
      }
      throw new TransportError(`${operation} received unexpected notification ${notification.operation}.`);
    };
    const message = (event: MessageEvent<unknown>) => {
      try {
        const frame = binaryFrameBytes(event.data);
        if (frame === undefined) {
          throw new TransportError(`${operation} subscription received a non-binary frame.`);
        }
        const envelope = decodeEnvelope(frame);
        switch (envelope.frame.case) {
          case "result":
          case "transportFailure":
            handleResponse(frame);
            return;
          case "notificationEvent":
            handleNotification(envelope.frame.value);
            return;
          case "call":
          case undefined:
            throw new TransportError(`${operation} subscription received an unexpected envelope.`);
        }
      } catch (cause) {
        socket.close();
        finish(cause instanceof Error ? cause : new TransportError(`${operation} subscription failed.`));
      }
    };
    const close = () => {
      if (signal.aborted) finish();
      else if (terminal !== null && acknowledged) settleTerminal();
      else finish(new TransportError("Subscription socket closed."));
    };
    const error = () => {
      finish(new TransportError("Subscription socket failed."));
    };
    const abort = () => {
      socket.close();
      finish();
    };
    socket.addEventListener("message", message);
    socket.addEventListener("close", close, { once: true });
    socket.addEventListener("error", error, { once: true });
    signal.addEventListener("abort", abort, { once: true });
    try {
      socket.send(binaryFramePayload(bytes));
    } catch (cause) {
      finish(cause instanceof Error ? cause : new TransportError(`${operation} request failed to send.`));
    }
  });
}

function requireAssociatedDescriptor(
  associated: DescMessage,
  projected: DescMessage,
  kind: "event" | "completion",
): void {
  if (associated !== projected) {
    throw new TransportError(`Descriptor subscription ${kind} projector does not match its association.`);
  }
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
  options: SocketRequestOptions,
): Promise<unknown> {
  const id = `${method}-${Date.now().toString()}`;
  return sendSocketFrame(
    socket,
    { label: method, frame: JSON.stringify({ jsonrpc: jsonRpcVersion, id, method, params }) },
    (event): SocketResponse<unknown> => {
      const textFrame = textFrameSchema.safeParse(event.data);
      if (!textFrame.success) {
        return { kind: "unmatched" };
      }
      const response = responseSchema.safeParse(parseFrame(textFrame.data));
      if (!response.success || response.data.id !== id) {
        return { kind: "unmatched" };
      }
      if (response.data.error !== undefined) {
        throw socketRequestError(method, response.data.error);
      }
      return { kind: "matched", result: response.data.result };
    },
    options,
  );
}

async function sendSocketFrame<Result>(
  socket: WebSocket,
  request: Readonly<{ label: string; frame: string | ArrayBuffer }>,
  decodeResponse: (event: MessageEvent<unknown>) => SocketResponse<Result>,
  options: SocketRequestOptions,
): Promise<Result> {
  const { frame, label } = request;
  const { signal, timeoutMilliseconds } = options;
  return new Promise((resolve, reject) => {
    if (signal?.aborted === true) {
      reject(new TransportError(`${label} request was canceled.`));
      return;
    }
    const timeout =
      timeoutMilliseconds === null
        ? null
        : setTimeout(() => {
            fail(new TransportError(`${label} request timed out.`));
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
      try {
        const response = decodeResponse(event);
        if (response.kind === "unmatched") {
          return;
        }
        cleanup();
        resolve(response.result);
      } catch (cause) {
        fail(cause instanceof Error ? cause : new TransportError(`${label} response decoding failed.`));
      }
    };
    const close = () => {
      fail(new TransportError(`${label} request closed before response.`));
    };
    const error = () => {
      fail(new TransportError(`${label} request failed before response.`));
    };
    const abort = () => {
      fail(new TransportError(`${label} request was canceled.`));
    };
    socket.addEventListener("message", listener);
    socket.addEventListener("close", close, { once: true });
    socket.addEventListener("error", error, { once: true });
    signal?.addEventListener("abort", abort, { once: true });
    try {
      socket.send(frame);
    } catch (cause) {
      fail(cause instanceof Error ? cause : new TransportError(`${label} request failed to send.`));
    }
  });
}

export function socketRequestError(
  method: string,
  error: Readonly<{ code: number; message: string; data?: JsonValue | undefined }>,
): Error {
  return new RpcError({ code: error.code, message: error.message, method, data: error.data });
}

function requireDescriptorSuccess(
  method: typeof ConnectionService.method.handshake | typeof ConnectionService.method.attachSession,
  result:
    | MessageShape<typeof ConnectionService.method.handshake.output>
    | MessageShape<typeof ConnectionService.method.attachSession.output>,
): void {
  switch (result.outcome.case) {
    case "success":
      return;
    case "error":
      if (
        method === ConnectionService.method.handshake &&
        result.outcome.value.code === "protocol_version_mismatch"
      ) {
        const detail = result.outcome.value.detail;
        if (detail.case !== "protocolVersionMismatch") {
          throw protobufRpcError(method, result.outcome.value);
        }
        throw new ProtocolMismatchError(detail.value.requiredProtocolVersion, protocolVersion);
      }
      throw protobufRpcError(method, result.outcome.value);
    case undefined:
      throw new TransportError(`${operationName(method)} returned no outcome.`);
  }
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
