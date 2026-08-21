import { ConnectionStore } from "./connectionStore";
import {
  binaryFrameBytes,
  binaryFramePayload,
  completeDescriptorResponse,
  decodeDescriptorResponse,
  descriptorResponseCorrelation,
  encodeDescriptorCall,
} from "./descriptorRpc";
import { TransportError } from "./errors";
import type { JsonValue } from "./json";
import { unaryConnectionPolicy, type DescMethod, type MessageShape } from "@app/server-api-contract";
import { z } from "zod";
import {
  delay,
  handleSubscriptionMessage,
  jsonRpcVersion,
  openSocket,
  parseFrame,
  responseSchema,
  sendSocketDescriptorRequest,
  sendSocketRequest,
  setupSocket,
  socketRequestError,
  subscriptionCompleteMethod,
  waitForSubscriptionEnd,
} from "./jsonRpcSocket";
import type {
  RpcCallOptions,
  DescriptorRpcTransport,
  RpcDedicatedCallOptions,
  RpcEventHandler,
  RpcSubscription,
  RpcTransport,
} from "./transport";

const socketOpenTimeoutMs = 10_000;
const rpcRequestTimeoutMs = 30_000;
const subscriptionReconnectBaseMs = 500;
const subscriptionReconnectMaxMs = 5_000;
const textFrameSchema = z.string();

type PendingRequestBase = Readonly<{
  label: string;
  timeout: ReturnType<typeof setTimeout> | null;
  reject(error: Error): void;
}>;

type PendingRequest = PendingRequestBase &
  Readonly<
    | { kind: "json"; resolve(value: unknown): void }
    | {
        kind: "descriptor";
        complete(response: ReturnType<typeof decodeDescriptorResponse>): void;
      }
  >;

export function createJsonRpcTransport(endpoint: string, expectedRootId = ""): DescriptorRpcTransport {
  return new JsonRpcWebSocketTransport(endpoint, expectedRootId);
}

class JsonRpcWebSocketTransport implements RpcTransport {
  readonly connection = new ConnectionStore();
  #endpoint: string;
  #expectedRootId: string;
  #socket: WebSocket | null = null;
  #opening: Promise<WebSocket> | null = null;
  #nextID = 1;
  #pending = new Map<string, PendingRequest>();

  constructor(endpoint: string, expectedRootId: string) {
    this.#endpoint = endpoint;
    this.#expectedRootId = expectedRootId;
  }

  async call(method: string, params: JsonValue, options?: RpcCallOptions): Promise<unknown> {
    const socket = await this.#open();
    return this.#send(socket, method, params, options);
  }

  async callDescriptor<Method extends DescMethod>(
    method: Method,
    request: MessageShape<Method["input"]>,
    options?: RpcCallOptions,
  ): Promise<MessageShape<Method["output"]>> {
    switch (unaryConnectionPolicy(method)) {
      case "multiplexed": {
        const socket = await this.#open();
        return this.#sendDescriptor(socket, method, request, options);
      }
      case "dedicated":
        return this.#withDedicatedSocket(options, async (socket, requestOptions) =>
          sendSocketDescriptorRequest(socket, method, request, requestOptions),
        );
    }
  }

  async callDedicated(
    method: string,
    params: JsonValue,
    options?: RpcDedicatedCallOptions,
  ): Promise<unknown> {
    return this.#withDedicatedSocket(options, async (socket, requestOptions) =>
      sendSocketRequest(socket, method, params, requestOptions),
    );
  }

  async callAttachedSession(
    sessionID: string,
    method: string,
    params: JsonValue,
    options?: RpcDedicatedCallOptions,
  ): Promise<unknown> {
    const attachedSessionID = sessionID.trim();
    if (attachedSessionID.length === 0) {
      throw new TransportError("Session attachment requires a Session ID.");
    }
    return this.#withDedicatedSocket(
      options,
      async (socket, requestOptions) => sendSocketRequest(socket, method, params, requestOptions),
      attachedSessionID,
    );
  }

  async #withDedicatedSocket<Result>(
    options: RpcDedicatedCallOptions | undefined,
    run: (
      socket: WebSocket,
      requestOptions: Readonly<{ timeoutMilliseconds: number | null; signal?: AbortSignal }>,
    ) => Promise<Result>,
    sessionID?: string,
  ): Promise<Result> {
    const socket = await openSocket(this.#endpoint, socketOpenTimeoutMs, options?.signal);
    try {
      await setupSocket(socket, {
        timeoutMilliseconds: rpcRequestTimeoutMs,
        expectedRootId: this.#expectedRootId,
        ...(options?.signal === undefined ? {} : { signal: options.signal }),
        ...(sessionID === undefined ? {} : { sessionID }),
      });
      const timeoutMs = options?.timeoutMs === undefined ? rpcRequestTimeoutMs : options.timeoutMs;
      const requestOptions =
        options?.signal === undefined
          ? { timeoutMilliseconds: timeoutMs }
          : { timeoutMilliseconds: timeoutMs, signal: options.signal };
      return await run(socket, requestOptions);
    } finally {
      socket.close();
    }
  }

  subscribe(method: string, params: JsonValue, handler: RpcEventHandler): RpcSubscription {
    const controller = new AbortController();
    void this.#openSubscription(method, params, handler, controller.signal);
    return {
      close() {
        controller.abort();
      },
    };
  }

  async #open(): Promise<WebSocket> {
    if (this.#socket?.readyState === WebSocket.OPEN) {
      return this.#socket;
    }
    if (this.#opening !== null) {
      return this.#opening;
    }
    this.connection.set("connecting");
    this.#opening = this.#connectControl();
    try {
      return await this.#opening;
    } finally {
      this.#opening = null;
    }
  }

  async #connectControl(): Promise<WebSocket> {
    const socket = await openSocket(this.#endpoint, socketOpenTimeoutMs);
    socket.addEventListener("message", (event) => {
      this.#handleControlMessage(event);
    });
    socket.addEventListener("close", () => {
      this.#handleControlClose();
    });
    socket.addEventListener("error", () => {
      this.#handleControlError();
    });
    try {
      await setupSocket(socket, {
        timeoutMilliseconds: rpcRequestTimeoutMs,
        expectedRootId: this.#expectedRootId,
      });
    } catch (error) {
      socket.close();
      throw error;
    }
    this.#socket = socket;
    this.connection.set("connected");
    return socket;
  }

  async #send(
    socket: WebSocket,
    method: string,
    params: JsonValue,
    options?: RpcCallOptions,
  ): Promise<unknown> {
    if (socket.readyState !== WebSocket.OPEN) {
      return Promise.reject(new TransportError("WebSocket is not open."));
    }
    const id = `gui-${this.#nextID.toString()}`;
    this.#nextID += 1;
    const frame = JSON.stringify({ jsonrpc: jsonRpcVersion, id, method, params });
    return new Promise((resolve, reject) => {
      const timeoutMs = options?.timeoutMs === undefined ? rpcRequestTimeoutMs : options.timeoutMs;
      const timeout =
        timeoutMs === null
          ? null
          : setTimeout(() => {
              if (!this.#pending.delete(id)) {
                return;
              }
              reject(new TransportError(`${method} request timed out.`));
            }, timeoutMs);
      this.#pending.set(id, { kind: "json", label: method, timeout, resolve, reject });
      try {
        socket.send(frame);
      } catch (error) {
        if (timeout !== null) {
          clearTimeout(timeout);
        }
        this.#pending.delete(id);
        reject(error instanceof Error ? error : new TransportError(`${method} request failed to send.`));
      }
    });
  }

  async #sendDescriptor<Method extends DescMethod>(
    socket: WebSocket,
    method: Method,
    request: MessageShape<Method["input"]>,
    options?: RpcCallOptions,
  ): Promise<MessageShape<Method["output"]>> {
    if (socket.readyState !== WebSocket.OPEN) {
      throw new TransportError("WebSocket is not open.");
    }
    const id = `gui-${this.#nextID.toString()}`;
    this.#nextID += 1;
    const { operation, bytes } = encodeDescriptorCall(method, request, id);
    return new Promise<MessageShape<Method["output"]>>((resolve, reject) => {
      const timeoutMs = options?.timeoutMs === undefined ? rpcRequestTimeoutMs : options.timeoutMs;
      const timeout =
        timeoutMs === null
          ? null
          : setTimeout(() => {
              if (!this.#pending.delete(id)) {
                return;
              }
              reject(new TransportError(`${operation} request timed out.`));
            }, timeoutMs);
      this.#pending.set(id, {
        kind: "descriptor",
        label: operation,
        timeout,
        complete: (response) => {
          resolve(completeDescriptorResponse(method, id, response));
        },
        reject,
      });
      try {
        socket.send(binaryFramePayload(bytes));
      } catch (error) {
        if (timeout !== null) {
          clearTimeout(timeout);
        }
        this.#pending.delete(id);
        reject(error instanceof Error ? error : new TransportError(`${operation} request failed to send.`));
      }
    });
  }

  #handleControlMessage(event: MessageEvent<unknown>): void {
    const textFrame = textFrameSchema.safeParse(event.data);
    if (textFrame.success) {
      const parsed = parseFrame(textFrame.data);
      const response = responseSchema.safeParse(parsed);
      if (!response.success || response.data.id === undefined) {
        return;
      }
      this.#resolveResponse(response.data.id, response.data.result, response.data.error);
      return;
    }
    const bytes = binaryFrameBytes(event.data);
    if (bytes === undefined) {
      this.#rejectAll(new TransportError("Unsupported WebSocket frame type."));
      return;
    }
    this.#handleBinaryControlMessage(bytes);
  }

  #handleBinaryControlMessage(bytes: Uint8Array): void {
    try {
      const response = decodeDescriptorResponse(bytes);
      const pending = this.#pending.get(response.correlation);
      if (pending === undefined) {
        return;
      }
      if (pending.kind !== "descriptor") {
        this.#takePending(response.correlation);
        pending.reject(new TransportError(`${pending.label} received a binary response.`));
        return;
      }
      try {
        pending.complete(response);
        this.#takePending(response.correlation);
      } catch (error) {
        this.#takePending(response.correlation);
        pending.reject(
          error instanceof Error ? error : new TransportError("Binary response completion failed."),
        );
      }
    } catch (error) {
      const correlation = descriptorResponseCorrelation(bytes);
      if (correlation === undefined) {
        return;
      }
      const pending = this.#takePending(correlation);
      pending?.reject(
        error instanceof Error ? error : new TransportError("Binary response decoding failed."),
      );
    }
  }

  #resolveResponse(
    id: string,
    result: unknown,
    error: { code: number; message: string; data?: JsonValue | undefined } | undefined,
  ): void {
    const pending = this.#pending.get(id);
    if (pending === undefined) {
      return;
    }
    this.#takePending(id);
    if (pending.kind !== "json") {
      pending.reject(new TransportError(`${pending.label} received a JSON response.`));
      return;
    }
    if (error !== undefined) {
      pending.reject(socketRequestError(pending.label, error));
      return;
    }
    pending.resolve(result);
  }

  #takePending(id: string): PendingRequest | undefined {
    const pending = this.#pending.get(id);
    if (pending === undefined) {
      return undefined;
    }
    this.#pending.delete(id);
    if (pending.timeout !== null) {
      clearTimeout(pending.timeout);
    }
    return pending;
  }

  #handleControlClose(): void {
    this.#socket = null;
    this.connection.set("disconnected", "Kent service connection closed.");
    this.#rejectAll(new TransportError("Kent service connection closed."));
  }

  #handleControlError(): void {
    this.#socket = null;
    this.connection.set("disconnected", "Kent service connection failed.");
    this.#rejectAll(new TransportError("Kent service connection failed."));
  }

  #rejectAll(error: Error): void {
    const pending = [...this.#pending.values()];
    this.#pending.clear();
    for (const request of pending) {
      if (request.timeout !== null) {
        clearTimeout(request.timeout);
      }
      request.reject(error);
    }
  }

  async #openSubscription(
    method: string,
    params: JsonValue,
    handler: RpcEventHandler,
    signal: AbortSignal,
  ): Promise<void> {
    let attempt = 0;
    while (!signal.aborted) {
      try {
        await this.#openSubscriptionSession(method, params, handler, signal);
        return;
      } catch (error) {
        if (abortSignalWasRequested(signal)) {
          return;
        }
        handler.onError(error instanceof Error ? error : new TransportError("Subscription failed."));
        await delay(Math.min(subscriptionReconnectBaseMs * 2 ** attempt, subscriptionReconnectMaxMs), signal);
        attempt += 1;
      }
    }
  }

  async #openSubscriptionSession(
    method: string,
    params: JsonValue,
    handler: RpcEventHandler,
    signal: AbortSignal,
  ): Promise<void> {
    const socket = await openSocket(this.#endpoint, socketOpenTimeoutMs);
    if (abortSignalWasRequested(signal)) {
      socket.close();
      return;
    }
    signal.addEventListener(
      "abort",
      () => {
        socket.close();
      },
      { once: true },
    );
    const terminalCompleteRef: { current: Readonly<{ code: number; message: string }> | null } = {
      current: null,
    };
    const completeMethod = subscriptionCompleteMethod(method);
    const subscriptionListener = (event: MessageEvent<unknown>) => {
      const result = handleSubscriptionMessage(event, handler, completeMethod);
      if (result.kind === "complete") {
        terminalCompleteRef.current = { code: result.code, message: result.message };
        socket.close();
      }
    };
    try {
      // The handshake must stay inside this scope: a rejected handshake (e.g. a
      // server reporting a different persistence root) would otherwise leave the
      // socket connected to the wrong server while the reconnect loop opens more.
      await setupSocket(socket, {
        timeoutMilliseconds: rpcRequestTimeoutMs,
        expectedRootId: this.#expectedRootId,
      });
      socket.addEventListener("message", subscriptionListener);
      await sendSocketRequest(socket, method, params, {
        timeoutMilliseconds: rpcRequestTimeoutMs,
      });
      handler.onOpen?.();
      await waitForSubscriptionEnd(socket, signal);
      this.#throwNonZeroComplete(method, terminalCompleteRef.current);
    } catch (error) {
      if (terminalCompleteRef.current?.code === 0) {
        return;
      }
      this.#throwNonZeroComplete(method, terminalCompleteRef.current);
      throw error;
    } finally {
      socket.removeEventListener("message", subscriptionListener);
      socket.close();
    }
  }

  #throwNonZeroComplete(method: string, complete: Readonly<{ code: number; message: string }> | null): void {
    if (complete === null || complete.code === 0) {
      return;
    }
    const suffix = complete.message.length === 0 ? "" : `: ${complete.message}`;
    throw new TransportError(
      `${method} subscription completed with code ${complete.code.toString()}${suffix}`,
    );
  }
}

function abortSignalWasRequested(signal: AbortSignal): boolean {
  return signal.aborted;
}
