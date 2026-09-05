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
} from "./descriptorRpc";
import {
  ContractError,
  ProtocolMismatchError,
  RpcError,
  ServerRootMismatchError,
  TransportError,
} from "./errors";
import { jsonValueSchema, type JsonValue } from "./json";
import { protobufRpcError } from "./protobufRpc";
import { requireProjectAttachment } from "./chatAttachment";
import type {
  DescriptorSubscriptionInput,
  ProjectAttachment,
  RpcEventHandler,
  SessionAttachment,
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

const notificationSchema = z
  .object({
    jsonrpc: z.literal(jsonRpcVersion),
    method: z.string().refine((value) => value.trim().length > 0),
    params: z.unknown(),
  })
  .strict();
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
>(
  input: DescriptorSubscriptionInput<Method, EventDescriptor, CompletionDescriptor> &
    Readonly<{
      socket: WebSocket;
      signal: AbortSignal;
    }>,
): Promise<void> {
  const { socket, method, request, eventDescriptor, completionDescriptor, onStart, handler, signal } = input;
  const associations = subscriptionAssociations(method);
  requireAssociatedDescriptor(associations.event.input, eventDescriptor, "event");
  requireAssociatedDescriptor(associations.completion.input, completionDescriptor, "completion");
  const correlation = `${method.name}-${Date.now().toString()}`;
  const { operation, bytes } = encodeDescriptorCall(method, request, correlation);
  return new Promise((resolve, reject) => {
    let acknowledged = false;
    let terminal = false;
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
    const handleResponse = (frame: Uint8Array) => {
      const response = decodeDescriptorResponse(frame);
      if (response.correlation !== correlation) return;
      onStart(completeDescriptorResponse(method, correlation, response));
      acknowledged = true;
      if (!terminal) handler.onOpen?.();
      else finish();
    };
    const handleNotification = (
      notification: Readonly<{ operation: string; payload?: Uint8Array | undefined }>,
    ) => {
      if (notification.payload === undefined) {
        throw new TransportError(`${notification.operation} notification payload is required.`);
      }
      if (notification.operation === operationName(associations.event)) {
        handler.onEvent(decode(eventDescriptor, notification.payload));
        return;
      }
      if (notification.operation === operationName(associations.completion)) {
        terminal = true;
        handler.onComplete(decode(completionDescriptor, notification.payload));
        socket.close();
        if (acknowledged) finish();
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
      else if (terminal && acknowledged) finish();
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
    projectSelector?: Readonly<{
      projectID: string;
      workspace: Readonly<{ workspaceID: string } | { workspaceRoot: string }>;
    }>;
  }>,
): Promise<ProjectAttachment | SessionAttachment | null> {
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
  let attachment: ProjectAttachment | SessionAttachment | null = null;
  if (options.projectSelector !== undefined) {
    const request = create(ConnectionService.method.attachProject.input, {
      projectId: options.projectSelector.projectID,
      workspace:
        "workspaceID" in options.projectSelector.workspace
          ? { case: "workspaceId", value: options.projectSelector.workspace.workspaceID }
          : { case: "workspaceRoot", value: options.projectSelector.workspace.workspaceRoot },
    });
    const result = await sendSocketDescriptorRequest(
      socket,
      ConnectionService.method.attachProject,
      request,
      requestOptions,
    );
    attachment = requireProjectAttachment(projectAttachmentFromResult(result), options.projectSelector);
  }
  if (options.sessionID !== undefined) {
    const attachment = await sendSocketDescriptorRequest(
      socket,
      ConnectionService.method.attachSession,
      create(ConnectionService.method.attachSession.input, { sessionId: options.sessionID }),
      requestOptions,
    );
    requireDescriptorSuccess(ConnectionService.method.attachSession, attachment);
    const attached = attachSessionFromResult(attachment);
    if (options.sessionID !== attached.sessionID) {
      throw new TransportError("Session attachment does not match its requested Session.");
    }
    return attached;
  }
  return attachment;
}

function projectAttachmentFromResult(
  result: MessageShape<typeof ConnectionService.method.attachProject.output>,
): ProjectAttachment {
  requireDescriptorSuccess(ConnectionService.method.attachProject, result);
  if (result.outcome.case !== "success" || result.outcome.value.attachment.case !== "project")
    throw new ContractError("Project attachment returned an unexpected attachment arm.");
  const attachment = result.outcome.value.attachment.value;
  const workspaceSelection = attachment.workspaceSelection;
  if (workspaceSelection.case === undefined) {
    throw new ContractError("Project attachment omitted its workspace selection.");
  }
  return {
    projectID: attachment.projectId,
    workspaceID: attachment.workspaceId,
    workspaceRoot: attachment.workspaceRoot,
    workspaceSelection:
      workspaceSelection.case === "selectedById"
        ? { kind: "workspaceID", workspaceID: workspaceSelection.value.workspaceId }
        : {
            kind: "workspaceRoot",
            requestedRoot: workspaceSelection.value.requestedRoot,
            canonicalRoot: workspaceSelection.value.canonicalRoot,
          },
  };
}

function attachSessionFromResult(
  result: MessageShape<typeof ConnectionService.method.attachSession.output>,
): SessionAttachment {
  if (result.outcome.case !== "success" || result.outcome.value.attachment.case !== "session") {
    throw new TransportError("Session attachment returned an unexpected attachment arm.");
  }
  const attachment = result.outcome.value.attachment.value;
  return {
    projectID: attachment.projectId,
    workspaceID: attachment.workspaceId,
    workspaceRoot: attachment.workspaceRoot,
    sessionID: attachment.sessionId,
  };
}

export function requireSessionAttachment(
  attachment: ProjectAttachment | SessionAttachment | null,
  target: Readonly<{ projectID?: string; sessionID: string }>,
): SessionAttachment {
  if (attachment === null || !("sessionID" in attachment)) {
    throw new ContractError("Session attachment was not established.");
  }
  if (attachment.sessionID !== target.sessionID) {
    throw new ContractError("Session attachment does not match the requested Session.");
  }
  if (target.projectID !== undefined && attachment.projectID !== target.projectID) {
    throw new ContractError("Session attachment does not match the requested Project.");
  }
  return attachment;
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
  method:
    | typeof ConnectionService.method.handshake
    | typeof ConnectionService.method.attachProject
    | typeof ConnectionService.method.attachSession,
  result:
    | MessageShape<typeof ConnectionService.method.handshake.output>
    | MessageShape<typeof ConnectionService.method.attachProject.output>
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
  { kind: "active" } | { kind: "complete"; code: number; message: string; reason: string | null }
>;

export function subscriptionCompleteMethod(subscriptionMethod: string): string | null {
  switch (subscriptionMethod) {
    case "workflow.subscribe":
      return "workflow.complete";
    case "workflow.subscribeProject":
      return "workflow.project.complete";
    case "attention.notification.subscribe":
      return "attention.notification.complete";
    case "session.subscribeTranscript":
      return "session.transcript.complete";
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
    throw new ContractError("Subscription received a non-text frame.");
  }
  const parsed = parseFrame(textFrame.data);
  const notification = notificationSchema.safeParse(parsed);
  if (!notification.success) {
    throw new ContractError("Subscription notification envelope is invalid.");
  }
  if (completeMethod !== null && notification.data.method === completeMethod) {
    const completeSchema = z
      .object({
        code: z.number().int().default(0),
        message: z.string().default(""),
        transcript_close_reason: z
          .enum(["subscriber_overflow", "contract_violation"])
          .nullable()
          .default(null),
      })
      .strict();
    const complete = completeSchema.safeParse(notification.data.params);
    if (!complete.success) {
      throw new ContractError("Subscription completion notification is invalid.");
    }
    handler.onComplete(complete.data.code, complete.data.message, complete.data.transcript_close_reason);
    return {
      kind: "complete",
      code: complete.data.code,
      message: complete.data.message,
      reason: complete.data.transcript_close_reason,
    };
  }
  try {
    handler.onEvent(notification.data.method, notification.data.params);
  } catch (error) {
    if (
      handler.onEventFailure?.(
        error instanceof Error ? error : new TransportError("Subscription event failed."),
      )
    ) {
      return { kind: "active" };
    }
    throw error;
  }
  return { kind: "active" };
}
