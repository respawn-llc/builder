import {
  ConnectionStore,
  type JsonValue,
  type LongRunningRpcMethod,
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

type RecordedCall =
  | Readonly<{ method: string; params: JsonValue; kind: "ordinary" }>
  | Readonly<{ method: LongRunningRpcMethod; params: JsonValue; kind: "long-running" }>;

export class FakeRpcTransport implements RpcTransport {
  readonly connection = new ConnectionStore();
  #calls: RecordedCall[] = [];
  #routes = new Map<string, FakeRoute>();
  #callCounts = new Map<string, number>();
  #subscribers: Readonly<{ method: string; params: JsonValue; handler: RpcEventHandler }>[] = [];

  constructor(routes: readonly FakeRoute[]) {
    for (const route of routes) {
      this.#routes.set(route.method, route);
    }
    this.connection.set("connected");
  }

  async call(method: string, params: JsonValue): Promise<unknown> {
    return this.#call({ method, params, kind: "ordinary" });
  }

  async callLongRunning(method: LongRunningRpcMethod, params: JsonValue): Promise<unknown> {
    return this.#call({ method, params, kind: "long-running" });
  }

  async #call(call: RecordedCall): Promise<unknown> {
    this.#calls.push(call);
    const route = this.#routes.get(call.method);
    if (route === undefined) {
      throw new Error(`Missing fake route: ${call.method}`);
    }
    if (route.error !== undefined) {
      throw route.error;
    }
    const callIndex = this.#callCounts.get(call.method) ?? 0;
    this.#callCounts.set(call.method, callIndex + 1);
    if (route.handler !== undefined) {
      return route.handler(call.params, callIndex);
    }
    return route.result;
  }

  get calls(): readonly Readonly<{ method: string; params: JsonValue }>[] {
    return this.#calls.flatMap((call) =>
      call.kind === "ordinary" ? [{ method: call.method, params: call.params }] : [],
    );
  }

  get longRunningCalls(): readonly Readonly<{ method: LongRunningRpcMethod; params: JsonValue }>[] {
    return this.#calls.flatMap((call) =>
      call.kind === "long-running" ? [{ method: call.method, params: call.params }] : [],
    );
  }

  get subscriptions(): Readonly<{ method: string; params: JsonValue }>[] {
    return this.#subscribers.map((subscriber) => ({
      method: subscriber.method,
      params: subscriber.params,
    }));
  }

  subscribe(method: string, params: JsonValue, handler: RpcEventHandler): RpcSubscription {
    const entry = { method, params, handler };
    this.#subscribers.push(entry);
    return {
      close: () => {
        this.#subscribers = this.#subscribers.filter((subscriber) => subscriber !== entry);
      },
    };
  }

  emit(method: string, params: unknown): void {
    for (const subscriber of [...this.#subscribers]) {
      subscriber.handler.onEvent(method, params);
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
