import { create } from "@app/server-api-contract";
import {
  SetupCompletionSchema,
  SetupEventSchema,
  SetupService,
  type SetupEvent,
} from "@app/server-api-contract/gen/kent/api/worktree/worktree_pb";

import { requireWorktreeSuccess } from "./clientWorktree";
import { ContractError } from "./errors";
import type { SetupOperationID } from "./setupOperationID";
import type { DescriptorRpcTransport, RpcSubscription } from "./transport";

export type WorktreeSetupEventHandler = Readonly<{
  onOpen?(): void;
  onEvent(event: SetupEvent): void;
  onComplete(): void;
  onError(error: Error): void;
}>;

export function subscribeWorktreeSetup(
  transport: DescriptorRpcTransport,
  setupOperationID: SetupOperationID,
  handler: WorktreeSetupEventHandler,
): RpcSubscription {
  let subscription: RpcSubscription | null = null;
  let finished = false;
  const finish = (notify?: () => void) => {
    if (finished) return;
    finished = true;
    subscription?.close();
    notify?.();
  };
  const method = SetupService.method.subscribe;
  subscription = transport.subscribeDescriptor(
    method,
    create(method.input, { setupOperationId: setupOperationID.toJSONValue() }),
    {
      eventDescriptor: SetupEventSchema,
      completionDescriptor: SetupCompletionSchema,
      projectStart(result) {
        requireWorktreeSuccess(method, result);
      },
      projectEvent(event) {
        return event;
      },
      classifyCompletion(completion) {
        return completion.code === undefined
          ? { kind: "normal" }
          : { kind: "error", code: completion.code, diagnostic: required(completion.diagnostic) };
      },
    },
    {
      ...(handler.onOpen === undefined
        ? {}
        : {
            onOpen() {
              if (!finished) handler.onOpen?.();
            },
          }),
      onEvent(event) {
        if (finished) return;
        handler.onEvent(event);
        if (event.phase.case !== "started") finish(handler.onComplete);
      },
      onTerminal(outcome) {
        if (outcome.kind === "normal") finish(handler.onComplete);
      },
      onError(error) {
        finish(() => {
          handler.onError(error);
        });
      },
    },
  );
  return { close: finish };
}

function required<Value>(value: Value | undefined): Value {
  if (value === undefined) throw new ContractError("Required Worktree setup fact is missing.");
  return value;
}
