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

export type DescriptorTerminalOutcome =
  Readonly<{ kind: "normal" }> | Readonly<{ kind: "error"; code: number; diagnostic: string }>;

export type DescriptorSubscriptionHandler<Event> = Readonly<{
  onOpen?(): void;
  onEvent(event: Event): void;
  onTerminal(outcome: DescriptorTerminalOutcome): void;
  onError(error: Error): void;
}>;

export type DescriptorSubscriptionContract<
  Method extends DescMethod,
  EventDescriptor extends DescMessage,
  CompletionDescriptor extends DescMessage,
  Event,
> = Readonly<{
  eventDescriptor: EventDescriptor;
  completionDescriptor: CompletionDescriptor;
  projectStart(result: MessageShape<Method["output"]>): void;
  projectEvent(message: MessageShape<EventDescriptor>): Event;
  classifyCompletion(message: MessageShape<CompletionDescriptor>): DescriptorTerminalOutcome;
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
      Event,
    >(
      method: Method,
      request: MessageShape<Method["input"]>,
      contract: DescriptorSubscriptionContract<Method, EventDescriptor, CompletionDescriptor, Event>,
      handler: DescriptorSubscriptionHandler<Event>,
    ): RpcSubscription;
  }>;
