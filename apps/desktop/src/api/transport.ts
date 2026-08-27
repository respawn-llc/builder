import type { ConnectionStore } from "./connectionStore";
import type { JsonValue } from "./json";
import type { DescMessage, DescMethod, MessageShape } from "@app/server-api-contract";

export type RpcEventHandler = Readonly<{
  onOpen?(): void;
  onEvent(method: string, params: unknown): void;
  onComplete(code: number, message: string): void;
  onError(error: Error): void;
}>;

export type RpcSubscription = Readonly<{
  close(): void;
}>;

export type DescriptorSubscriptionHandler<Event, Completion> = Readonly<{
  onOpen?(): void;
  onEvent(event: Event): void;
  onComplete(completion: Completion): void;
  onError(error: Error): void;
}>;

export type DescriptorSubscriptionInput<
  Method extends DescMethod,
  EventDescriptor extends DescMessage,
  CompletionDescriptor extends DescMessage,
> = Readonly<{
  method: Method;
  request: MessageShape<Method["input"]>;
  eventDescriptor: EventDescriptor;
  completionDescriptor: CompletionDescriptor;
  onStart(result: MessageShape<Method["output"]>): void;
  handler: DescriptorSubscriptionHandler<MessageShape<EventDescriptor>, MessageShape<CompletionDescriptor>>;
}>;

export type RpcCallOptions = Readonly<{
  timeoutMs?: number | null;
}>;

export type RpcDedicatedCallOptions = RpcCallOptions &
  Readonly<{
    signal?: AbortSignal;
  }>;

export type RpcTransport = Readonly<{
  connection: ConnectionStore;
  call(method: string, params: JsonValue, options?: RpcCallOptions): Promise<unknown>;
  callDedicated(method: string, params: JsonValue, options?: RpcDedicatedCallOptions): Promise<unknown>;
  callAttachedSession(
    sessionID: string,
    method: string,
    params: JsonValue,
    options?: RpcDedicatedCallOptions,
  ): Promise<unknown>;
  subscribe(method: string, params: JsonValue, handler: RpcEventHandler): RpcSubscription;
}>;

export type DescriptorRpcTransport = RpcTransport &
  Readonly<{
    callDescriptor<Method extends DescMethod>(
      method: Method,
      request: MessageShape<Method["input"]>,
      options?: RpcCallOptions,
    ): Promise<MessageShape<Method["output"]>>;
    subscribeDescriptor<
      Method extends DescMethod,
      EventDescriptor extends DescMessage,
      CompletionDescriptor extends DescMessage,
    >(
      input: DescriptorSubscriptionInput<Method, EventDescriptor, CompletionDescriptor>,
    ): RpcSubscription;
  }>;
