import {
  create,
  decode,
  encode,
  operationName,
  type DescMessage,
  type DescMethod,
  type Message,
  type MessageShape,
} from "@app/server-api-contract";
import { ProjectAvailability } from "@app/server-api-contract/gen/kent/api/project/project_pb";
import {
  BranchCleanupOutcomeKind,
  CreateResultSchema,
  CreateService,
  CreateTargetResolutionKind,
  CreateTargetResolveResultSchema,
  CreateTargetService,
  DeletePreviewResultSchema,
  DeletePreviewService,
  DeleteResultSchema,
  DirtyStateKind,
  EnterResultSchema,
  SelectorResolveResultSchema,
  SelectorService,
  SwitchOperationKind,
  TransitionService,
} from "@app/server-api-contract/gen/kent/api/worktree/worktree_pb";
import {
  ConnectionStore,
  type DescriptorRpcTransport,
  type JsonValue,
  type RpcCallOptions,
  type RpcDedicatedCallOptions,
  type RpcEventHandler,
  type RpcSubscription,
} from "@/api/composition";

type FakeJsonRoute = Readonly<{
  method: string;
  result?: unknown;
  error?: Error;
  handler?: (params: JsonValue, callIndex: number) => unknown;
}>;

type FakeDescriptorRoute = Readonly<{
  descriptor: DescMethod;
  result?: Message;
  error?: Error;
  resultFactory?: (request: Message, callIndex: number) => Message;
}>;

type FakeDescriptorSubscriptionRoute = Readonly<{
  subscriptionDescriptor: DescMethod;
  startResult: Message;
}>;

export type FakeRoute = FakeJsonRoute | FakeDescriptorRoute | FakeDescriptorSubscriptionRoute;

export function worktreeQueryFixtureRoutes(): readonly FakeRoute[] {
  const topology = {
    topology: {
      case: "external",
      value: {
        git: {
          canonicalRoot: "/repo/feature",
          headObject: "abc123",
          branchName: "feature",
          detached: false,
          bare: false,
          isMain: false,
          pathAvailable: true,
        },
      },
    },
  } as const;
  const projected = (selector: string) => ({
    topology,
    projection: {
      selector,
      isCurrent: false,
      switch: {
        kind: SwitchOperationKind.WORKTREE_SWITCH_OPERATION_ENTER,
        selector,
      },
      deletePreview: { selector: "/repo/feature" },
    },
  });
  return [
    {
      descriptor: CreateTargetService.method.resolve,
      resultFactory: (_request, callIndex) =>
        create(CreateTargetResolveResultSchema, {
          outcome: {
            case: "success",
            value: {
              resolution: {
                kind: CreateTargetResolutionKind.WORKTREE_CREATE_TARGET_RESOLUTION_KIND_EXISTING_BRANCH,
                input: "feature",
                resolvedRef: `refs/heads/feature-${String(callIndex)}`,
              },
            },
          },
        }),
    },
    {
      descriptor: SelectorService.method.resolve,
      resultFactory: (_request, callIndex) =>
        create(SelectorResolveResultSchema, {
          outcome: {
            case: "success",
            value: { worktree: projected(`feature-${String(callIndex)}`) },
          },
        }),
    },
    {
      descriptor: DeletePreviewService.method.get,
      resultFactory: (_request, callIndex) =>
        create(DeletePreviewResultSchema, {
          outcome: {
            case: "success",
            value: {
              worktree: topology,
              deletionSelector: "/repo/feature",
              cleanliness:
                callIndex === 0
                  ? { kind: DirtyStateKind.DIRTY_STATE_CLEAN }
                  : { kind: DirtyStateKind.DIRTY_STATE_DIRTY, dirtyFileCount: callIndex },
            },
          },
        }),
    },
    {
      descriptor: CreateService.method.create,
      result: create(CreateResultSchema, {
        outcome: {
          case: "success",
          value: {
            target: {
              workspaceId: "workspace-1",
              workspaceName: "Workspace",
              workspaceRoot: "/repo",
              workspaceAvailability: ProjectAvailability.AVAILABLE,
              cwdRelpath: ".",
              effectiveWorkdir: "/repo",
            },
            worktree: projected("feature"),
          },
        },
      }),
    },
    {
      descriptor: TransitionService.method.enter,
      result: create(EnterResultSchema, {
        outcome: {
          case: "error",
          value: {
            code: "internal_failure",
            detail: {
              case: "internalFailure",
              value: { operation: "worktree.enter", cause: "fixture failure" },
            },
          },
        },
      }),
    },
    {
      descriptor: TransitionService.method.delete,
      result: create(DeleteResultSchema, {
        outcome: {
          case: "success",
          value: {
            cleanup: {
              kind: BranchCleanupOutcomeKind.WORKTREE_BRANCH_CLEANUP_OUTCOME_NOT_REQUESTED,
            },
          },
        },
      }),
    },
  ];
}

export class FakeRpcTransport implements DescriptorRpcTransport {
  readonly connection = new ConnectionStore();
  readonly calls: Readonly<{ method: string; params: JsonValue; options?: RpcCallOptions }>[] = [];
  readonly descriptorCalls: Readonly<{
    descriptor: DescMethod;
    request: Message;
    options?: RpcCallOptions;
  }>[] = [];
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
  readonly descriptorSubscriptionStarts: Readonly<{
    descriptor: DescMethod;
    request: Message;
  }>[] = [];
  #routes = new Map<string, FakeJsonRoute>();
  #descriptorRoutes = new Map<string, FakeDescriptorRoute>();
  #descriptorSubscriptionRoutes = new Map<string, FakeDescriptorSubscriptionRoute>();
  #callCounts = new Map<string, number>();
  #subscribers: Readonly<{ method: string; params: JsonValue; handler: RpcEventHandler }>[] = [];
  #descriptorSubscribers: Readonly<{
    descriptor: DescMethod;
    open(): void;
    event(payload: Uint8Array): void;
    complete(payload: Uint8Array): void;
    fail(error: Error): void;
  }>[] = [];

  constructor(routes: readonly FakeRoute[]) {
    for (const route of routes) {
      if ("subscriptionDescriptor" in route) {
        this.#descriptorSubscriptionRoutes.set(operationName(route.subscriptionDescriptor), route);
      } else if ("descriptor" in route) {
        this.#descriptorRoutes.set(operationName(route.descriptor), route);
      } else {
        this.#routes.set(route.method, route);
      }
    }
    this.connection.set("connected");
  }

  async call(method: string, params: JsonValue, options?: RpcCallOptions): Promise<unknown> {
    this.calls.push(options === undefined ? { method, params } : { method, params, options });
    return this.#dispatch(method, params);
  }

  async callDescriptor<Method extends DescMethod>(
    descriptor: Method,
    request: MessageShape<Method["input"]>,
    options?: RpcCallOptions,
  ): Promise<MessageShape<Method["output"]>> {
    this.descriptorCalls.push(
      options === undefined ? { descriptor, request } : { descriptor, request, options },
    );
    const operation = operationName(descriptor);
    const route = this.#descriptorRoutes.get(operation);
    if (route === undefined) {
      throw new Error(`Missing fake descriptor route: ${operation}`);
    }
    if (route.error !== undefined) {
      throw route.error;
    }
    const callIndex = this.#callCounts.get(operation) ?? 0;
    this.#callCounts.set(operation, callIndex + 1);
    const result = route.resultFactory?.(request, callIndex) ?? route.result;
    if (result === undefined) {
      throw new Error(`Missing fake descriptor result: ${operation}`);
    }
    const payload = encode(route.descriptor.output, result);
    return decode<Method["output"]>(descriptor.output, payload);
  }

  subscribeDescriptor<
    Method extends DescMethod,
    EventDescriptor extends DescMessage,
    CompletionDescriptor extends DescMessage,
  >(
    ...args: [
      descriptor: Method,
      request: MessageShape<Method["input"]>,
      eventDescriptor: EventDescriptor,
      completionDescriptor: CompletionDescriptor,
      onStart: (result: MessageShape<Method["output"]>) => void,
      handler: Readonly<{
        onOpen?(): void;
        onEvent(event: MessageShape<EventDescriptor>): void;
        onComplete(completion: MessageShape<CompletionDescriptor>): void;
        onError(error: Error): void;
      }>,
    ]
  ): RpcSubscription {
    const [descriptor, request, eventDescriptor, completionDescriptor, onStart, handler] = args;
    const operation = operationName(descriptor);
    const route = this.#descriptorSubscriptionRoutes.get(operation);
    if (route === undefined) throw new Error(`Missing fake descriptor subscription route: ${operation}`);
    this.descriptorSubscriptionStarts.push({ descriptor, request });
    const entry = {
      descriptor,
      open: () => {
        try {
          onStart(
            decode<Method["output"]>(
              descriptor.output,
              encode(route.subscriptionDescriptor.output, route.startResult),
            ),
          );
          handler.onOpen?.();
        } catch (cause) {
          handler.onError(cause instanceof Error ? cause : new Error("Descriptor subscription failed."));
        }
      },
      event: (payload: Uint8Array) => {
        try {
          handler.onEvent(decode(eventDescriptor, payload));
        } catch (cause) {
          handler.onError(cause instanceof Error ? cause : new Error("Descriptor event failed."));
        }
      },
      complete: (payload: Uint8Array) => {
        handler.onComplete(decode(completionDescriptor, payload));
      },
      fail: handler.onError,
    };
    this.#descriptorSubscribers.push(entry);
    return {
      close: () => {
        this.#descriptorSubscribers = this.#descriptorSubscribers.filter(
          (subscriber) => subscriber !== entry,
        );
      },
    };
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

  openDescriptor(descriptor: DescMethod): void {
    for (const subscriber of this.#descriptorSubscribersFor(descriptor)) subscriber.open();
  }

  get descriptorSubscriptions(): readonly DescMethod[] {
    return this.#descriptorSubscribers.map(({ descriptor }) => descriptor);
  }

  emitDescriptor<EventMethod extends DescMethod>(
    subscriptionDescriptor: DescMethod,
    eventDescriptor: EventMethod,
    message: MessageShape<EventMethod["input"]>,
  ): void {
    const payload = encode(eventDescriptor.input, message);
    for (const subscriber of this.#descriptorSubscribersFor(subscriptionDescriptor)) {
      subscriber.event(payload);
    }
  }

  emitDescriptorBytes(subscriptionDescriptor: DescMethod, payload: Uint8Array): void {
    for (const subscriber of this.#descriptorSubscribersFor(subscriptionDescriptor)) {
      subscriber.event(payload);
    }
  }

  completeDescriptor<CompletionMethod extends DescMethod>(
    subscriptionDescriptor: DescMethod,
    completionDescriptor: CompletionMethod,
    message: MessageShape<CompletionMethod["input"]>,
  ): void {
    const payload = encode(completionDescriptor.input, message);
    for (const subscriber of this.#descriptorSubscribersFor(subscriptionDescriptor)) {
      subscriber.complete(payload);
    }
  }

  failDescriptor(descriptor: DescMethod, error: Error): void {
    for (const subscriber of this.#descriptorSubscribersFor(descriptor)) subscriber.fail(error);
  }

  #subscribersFor(
    subscriptionMethod: string,
  ): readonly Readonly<{ method: string; params: JsonValue; handler: RpcEventHandler }>[] {
    return this.#subscribers.filter((subscriber) => subscriber.method === subscriptionMethod);
  }

  #descriptorSubscribersFor(descriptor: DescMethod) {
    const operation = operationName(descriptor);
    return this.#descriptorSubscribers.filter(
      (subscriber) => operationName(subscriber.descriptor) === operation,
    );
  }
}
