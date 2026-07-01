import { ConnectionStore } from "./connectionStore";
import type { JsonValue } from "./json";
import type { RpcEventHandler, RpcSubscription, RpcTransport } from "./transport";

export type FakeRoute = Readonly<{
  method: string;
  result?: unknown;
  error?: Error;
  handler?: (params: JsonValue, callIndex: number) => unknown;
}>;

export class FakeRpcTransport implements RpcTransport {
  readonly connection = new ConnectionStore();
  readonly calls: Readonly<{ method: string; params: JsonValue }>[] = [];
  #routes = new Map<string, FakeRoute>();
  #callCounts = new Map<string, number>();
  #subscribers: Readonly<{ method: string; handler: RpcEventHandler }>[] = [];

  constructor(routes: readonly FakeRoute[]) {
    for (const route of routes) {
      this.#routes.set(route.method, route);
    }
    this.connection.set("connected");
  }

  async call(method: string, params: JsonValue): Promise<unknown> {
    this.calls.push({ method, params });
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

  subscribe(method: string, _params: JsonValue, handler: RpcEventHandler): RpcSubscription {
    const entry = { method, handler };
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
