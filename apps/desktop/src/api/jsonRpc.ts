import { ConnectionStore } from "./connectionStore";
import { TransportError } from "./errors";
import type { JsonValue } from "./json";
import {
  assertHandshakeRoot,
  delay,
  handleSubscriptionMessage,
  handshakeMethod,
  handshakeSubscription,
  jsonRpcVersion,
  openSocket,
  parseFrame,
  protocolVersion,
  responseSchema,
  sendSocketRequest,
  socketRequestError,
  subscriptionCompleteMethod,
  waitForSubscriptionEnd,
} from "./jsonRpcSocket";
import type { RpcEventHandler, RpcSubscription, RpcTransport } from "./transport";

const socketOpenTimeoutMs = 10_000;
const rpcRequestTimeoutMs = 30_000;
const subscriptionReconnectBaseMs = 500;
const subscriptionReconnectMaxMs = 5_000;

type PendingRequest = Readonly<{
  method: string;
  timeout: ReturnType<typeof setTimeout>;
  resolve(value: unknown): void;
  reject(error: Error): void;
}>;

export function createJsonRpcTransport(endpoint: string, expectedRootId = ""): RpcTransport {
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

  async call(method: string, params: JsonValue): Promise<unknown> {
    const socket = await this.#open();
    return this.#send(socket, method, params);
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
      const result = await this.#send(socket, handshakeMethod, { protocol_version: protocolVersion });
      assertHandshakeRoot(result, this.#expectedRootId);
    } catch (error) {
      socket.close();
      throw error;
    }
    this.#socket = socket;
    this.connection.set("connected");
    return socket;
  }

  async #send(socket: WebSocket, method: string, params: JsonValue): Promise<unknown> {
    if (socket.readyState !== WebSocket.OPEN) {
      return Promise.reject(new TransportError("WebSocket is not open."));
    }
    const id = `gui-${this.#nextID.toString()}`;
    this.#nextID += 1;
    const frame = JSON.stringify({ jsonrpc: jsonRpcVersion, id, method, params });
    return new Promise((resolve, reject) => {
      const timeout = setTimeout(() => {
        if (!this.#pending.delete(id)) {
          return;
        }
        reject(new TransportError(`${method} request timed out.`));
      }, rpcRequestTimeoutMs);
      this.#pending.set(id, { method, timeout, resolve, reject });
      try {
        socket.send(frame);
      } catch (error) {
        clearTimeout(timeout);
        this.#pending.delete(id);
        reject(error instanceof Error ? error : new TransportError(`${method} request failed to send.`));
      }
    });
  }

  #handleControlMessage(event: MessageEvent<unknown>): void {
    if (typeof event.data !== "string") {
      this.#rejectAll(new TransportError("Unsupported WebSocket frame type."));
      return;
    }
    const parsed = parseFrame(event.data);
    const response = responseSchema.safeParse(parsed);
    if (!response.success || response.data.id === undefined) {
      return;
    }
    this.#resolveResponse(response.data.id, response.data.result, response.data.error);
  }

  #resolveResponse(id: string, result: unknown, error: { code: number; message: string } | undefined): void {
    const pending = this.#pending.get(id);
    if (pending === undefined) {
      return;
    }
    this.#pending.delete(id);
    clearTimeout(pending.timeout);
    if (error !== undefined) {
      pending.reject(socketRequestError(pending.method, error));
      return;
    }
    pending.resolve(result);
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
      clearTimeout(request.timeout);
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
      await handshakeSubscription(socket, rpcRequestTimeoutMs, this.#expectedRootId);
      socket.addEventListener("message", subscriptionListener);
      await sendSocketRequest(socket, method, params, rpcRequestTimeoutMs);
      handler.onOpen?.();
      await waitForSubscriptionEnd(socket, signal);
    } catch (error) {
      if (terminalCompleteRef.current?.code === 0) {
        return;
      }
      throw error;
    } finally {
      socket.removeEventListener("message", subscriptionListener);
      socket.close();
    }
  }
}

function abortSignalWasRequested(signal: AbortSignal): boolean {
  return signal.aborted;
}
