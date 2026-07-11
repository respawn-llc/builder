import { ConnectionStore } from "./connectionStore";
import type { JsonValue } from "./json";
import type { RpcEventHandler, RpcSubscription, RpcTransport } from "./transport";
import type { RpcCallOptions } from "./transport";

export type FakeRoute = Readonly<{
  method: string;
  result?: unknown;
  error?: Error;
  handler?: (params: JsonValue, callIndex: number) => unknown;
}>;

export class FakeRpcTransport implements RpcTransport {
  readonly connection = new ConnectionStore();
  readonly calls: Readonly<{ method: string; params: JsonValue; options?: RpcCallOptions }>[] = [];
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
    this.#subscribers.push(entry);
    return {
      close: () => {
        this.#subscribers = this.#subscribers.filter((subscriber) => subscriber !== entry);
      },
    };
  }

  // emit delivers an event to every open subscription, letting tests drive the
  // event-stream code paths (live refresh, attention updates) deterministically.
  emit(method: string, params: unknown): void {
    for (const subscriber of [...this.#subscribers]) {
      subscriber.handler.onEvent(method, params);
    }
  }
}
