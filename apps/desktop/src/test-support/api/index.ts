import {
  ConnectionStore,
  type JsonValue,
  type RpcCallOptions,
  type RpcDedicatedCallOptions,
  type RpcEventHandler,
  type RpcSubscription,
  type RpcTransport,
} from "@/api/composition";

export type FakeRoute = Readonly<{
  method: string;
  result?: unknown;
  error?: Error;
  handler?: (params: JsonValue, callIndex: number) => unknown;
}>;

export class FakeRpcTransport implements RpcTransport {
  readonly connection = new ConnectionStore();
  readonly calls: Readonly<{ method: string; params: JsonValue; options?: RpcCallOptions }>[] = [];
  readonly dedicatedCalls: Readonly<{
    method: string;
    params: JsonValue;
    options?: RpcDedicatedCallOptions;
  }>[] = [];
  readonly attachedSessionCalls: Readonly<{
    sessionID: string;
    method: string;
    params: JsonValue;
    options?: RpcDedicatedCallOptions;
  }>[] = [];
  readonly subscriptionStarts: Readonly<{ method: string; params: JsonValue }>[] = [];
  #routes = new Map<string, FakeRoute>();
  #callCounts = new Map<string, number>();
  #subscribers: Readonly<{ method: string; params: JsonValue; handler: RpcEventHandler }>[] = [];

  constructor(routes: readonly FakeRoute[]) {
    for (const route of routes) {
      this.#routes.set(route.method, route);
    }
    this.connection.set("connected");
  }

  async call(method: string, params: JsonValue, options?: RpcCallOptions): Promise<unknown> {
    this.calls.push(options === undefined ? { method, params } : { method, params, options });
    return this.#dispatch(method, params);
  }

  async callDedicated(
    method: string,
    params: JsonValue,
    options?: RpcDedicatedCallOptions,
  ): Promise<unknown> {
    this.dedicatedCalls.push(options === undefined ? { method, params } : { method, params, options });
    return this.#dispatch(method, params);
  }

  async callAttachedSession(
    sessionID: string,
    method: string,
    params: JsonValue,
    options?: RpcDedicatedCallOptions,
  ): Promise<unknown> {
    this.attachedSessionCalls.push(
      options === undefined ? { sessionID, method, params } : { sessionID, method, params, options },
    );
    return this.#dispatch(method, params);
  }

  #dispatch(method: string, params: JsonValue): unknown {
    const route = this.#routes.get(method);
    if (route === undefined) {
      throw new Error(`Missing fake route: ${method}`);
    }
    if (route.error !== undefined) {
      throw route.error;
    }
    const callIndex = this.#callCounts.get(method) ?? 0;
    this.#callCounts.set(method, callIndex + 1);
    if (route.handler !== undefined) {
      return route.handler(params, callIndex);
    }
    return route.result;
  }

  get subscriptions(): Readonly<{ method: string; params: JsonValue }>[] {
    return this.#subscribers.map((subscriber) => ({
      method: subscriber.method,
      params: subscriber.params,
    }));
  }

  subscribe(method: string, params: JsonValue, handler: RpcEventHandler): RpcSubscription {
    const entry = { method, params, handler };
    this.subscriptionStarts.push({ method, params });
    this.#subscribers.push(entry);
    return {
      close: () => {
        this.#subscribers = this.#subscribers.filter((subscriber) => subscriber !== entry);
      },
    };
  }

  emit(method: string, params: unknown): void {
    for (const subscriber of [...this.#subscribers]) {
      const error = subscriber.handler.onEvent(method, params);
      if (error instanceof Error) {
        subscriber.handler.onError(error);
        this.#subscribers = this.#subscribers.filter((candidate) => candidate !== subscriber);
      }
    }
  }

  open(subscriptionMethod: string): void {
    for (const subscriber of this.#subscribersFor(subscriptionMethod)) {
      subscriber.handler.onOpen?.();
    }
  }

  complete(subscriptionMethod: string, code: number, message: string): void {
    for (const subscriber of this.#subscribersFor(subscriptionMethod)) {
      subscriber.handler.onComplete(code, message);
    }
  }

  fail(subscriptionMethod: string, error: Error): void {
    for (const subscriber of this.#subscribersFor(subscriptionMethod)) {
      subscriber.handler.onError(error);
    }
  }

  #subscribersFor(
    subscriptionMethod: string,
  ): readonly Readonly<{ method: string; params: JsonValue; handler: RpcEventHandler }>[] {
    return this.#subscribers.filter((subscriber) => subscriber.method === subscriptionMethod);
  }
}
