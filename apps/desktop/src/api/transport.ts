import type { ConnectionStore } from "./connectionStore";
import type { JsonValue } from "./json";

export type RpcEventHandler = Readonly<{
  onOpen?(): void;
  onEvent(method: string, params: unknown): void;
  onComplete(code: number, message: string): void;
  onError(error: Error): void;
}>;

export type RpcSubscription = Readonly<{
  close(): void;
}>;

export type RpcCallOptions = Readonly<{
  timeoutMs?: number | null;
}>;

export type RpcTransport = Readonly<{
  connection: ConnectionStore;
  call(method: string, params: JsonValue, options?: RpcCallOptions): Promise<unknown>;
  subscribe(method: string, params: JsonValue, handler: RpcEventHandler): RpcSubscription;
}>;
