import { create } from "@app/server-api-contract";
import {
  SetupCompletionSchema,
  SetupEventSchema,
  SetupService,
  type SetupEvent,
} from "@app/server-api-contract/gen/kent/api/worktree/worktree_pb";

import { requireWorktreeSuccess } from "./clientWorktree";
import { ContractError, TransportError } from "./errors";
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
  subscription = transport.subscribeDescriptor({
    method,
    request: create(method.input, { setupOperationId: setupOperationID.toJSONValue() }),
    eventDescriptor: SetupEventSchema,
    completionDescriptor: SetupCompletionSchema,
    onStart(result) {
      requireWorktreeSuccess(method, result);
    },
    handler: {
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
      onComplete(completion) {
        const code = completion.code;
        if (code === undefined) {
          finish(handler.onComplete);
          return;
        }
        const diagnostic = required(completion.diagnostic);
        finish(() => {
          handler.onError(
            new TransportError(`Worktree setup completed with code ${code.toString()}: ${diagnostic}`),
          );
        });
      },
      onError(error) {
        finish(() => {
          handler.onError(error);
        });
      },
    },
  });
  return { close: finish };
}

function required<Value>(value: Value | undefined): Value {
  if (value === undefined) throw new ContractError("Required Worktree setup fact is missing.");
  return value;
}
