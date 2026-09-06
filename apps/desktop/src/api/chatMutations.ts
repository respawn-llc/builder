import { create } from "@app/server-api-contract";
import {
  ChatService,
  type AcceptedDiagnostic,
  type CompactionNotAccepted,
  type InputNotAccepted,
} from "@app/server-api-contract/gen/kent/api/chat/chat_pb";
import type { SupervisorValue } from "@app/server-api-contract/gen/kent/api/chat_settings/chat_settings_pb";
import {
  LiveService,
  LiveStopStatus,
  PendingWorkItemKind,
  PendingWorkItemState,
  PendingWorkLane,
  PendingWorkWorktreeTransitionKind,
  TurnService,
  type PendingWork as WirePendingWork,
} from "@app/server-api-contract/gen/kent/api/runtime/runtime_pb";
import {
  SessionLifecycleService,
  SessionTransitionAction,
} from "@app/server-api-contract/gen/kent/api/session_launch/session_lifecycle_pb";

import { requireProjectAttachment } from "./chatAttachment";
import { requireChatSuccess } from "./chatErrors";
import { supervisorToWire } from "./chatSupervisor";
import { nonBlank } from "./chatSchemas";
import { requireChatProjectTarget, requireChatSessionID } from "./chatTarget";
import type { ChatActivation, ChatApi, ChatMutationTarget } from "./chatTypes";
import { ContractError } from "./errors";
import {
  parseCompactionRequestID,
  parsePendingWorkItemID,
  pendingWorkRestorationSchema,
  pendingWorkSchema,
} from "./pendingWork";
import type { DescriptorRpcTransport } from "./transport";

type ChatMutationApi = Pick<
  ChatApi,
  "steer" | "queue" | "compact" | "stop" | "forkEdit" | "listPendingWork" | "removePendingWork"
>;

export function createChatMutationApi(transport: DescriptorRpcTransport): ChatMutationApi {
  const mutateInput = async (
    method: typeof ChatService.method.steer | typeof ChatService.method.queue,
    target: ChatMutationTarget,
    activation: ChatActivation,
  ) => {
    const call = await transport.callDescriptorAttachedProject({
      projectID: target.projectID,
      selector: target.workspace,
      method,
      createRequest: (attachment) =>
        create(method.input, {
          target: mutationTarget(target, attachment.workspaceID),
          activation: chatActivation(activation),
        }),
    });
    requireProjectAttachment(call.attachment, target);
    const success = requireChatSuccess(method, call.result);
    const returnedSessionID = mutationSessionID(target, success.session?.sessionId, "Chat mutation");
    if (success.outcome.case === "notAccepted") {
      return {
        sessionID: returnedSessionID,
        outcome: {
          kind: "not_accepted" as const,
          reason: inputNotAccepted(success.outcome.value.reason),
        },
      };
    }
    if (success.outcome.case !== "accepted" || success.outcome.value.queueItem === undefined) {
      throw new ContractError("Chat mutation response did not match the GUI contract.");
    }
    return {
      sessionID: returnedSessionID,
      outcome: {
        kind: "accepted" as const,
        queueItemID: parsePendingWorkItemID(success.outcome.value.queueItem.id),
        diagnostic: acceptedDiagnostic(success.outcome.value.diagnostic),
      },
    };
  };

  return {
    steer: async (target, activation) => mutateInput(ChatService.method.steer, target, activation),
    queue: async (target, activation) => mutateInput(ChatService.method.queue, target, activation),
    async compact(target, invocation) {
      const method = ChatService.method.compact;
      const call = await transport.callDescriptorAttachedProject({
        projectID: target.projectID,
        selector: target.workspace,
        method,
        createRequest: (attachment) =>
          create(method.input, {
            target: mutationTarget(target, attachment.workspaceID),
            invocation,
          }),
      });
      requireProjectAttachment(call.attachment, target);
      const success = requireChatSuccess(method, call.result);
      const returnedSessionID = mutationSessionID(target, success.session?.sessionId, "Chat compaction");
      if (success.outcome.case === "notAccepted") {
        return {
          sessionID: returnedSessionID,
          outcome: {
            kind: "not_accepted",
            reason: compactionNotAccepted(success.outcome.value.reason),
          },
        };
      }
      if (success.outcome.case !== "accepted" || success.outcome.value.request === undefined) {
        throw new ContractError("Chat compaction response did not match the GUI contract.");
      }
      return {
        sessionID: returnedSessionID,
        outcome: {
          kind: "accepted",
          requestID: parseCompactionRequestID(success.outcome.value.request.id),
          diagnostic: acceptedDiagnostic(success.outcome.value.diagnostic),
        },
      };
    },
    async stop(target) {
      const requestedSessionID = requireChatSessionID(target);
      const method = LiveService.method.stop;
      const call = await transport.callDescriptorAttachedProject({
        projectID: target.projectID,
        selector: target.workspace,
        method,
        createRequest: () => create(method.input, { sessionId: requestedSessionID }),
      });
      requireProjectAttachment(call.attachment, target);
      const success = requireChatSuccess(method, call.result);
      switch (success.status) {
        case LiveStopStatus.RUNTIME_LIVE_STOP_STATUS_STOPPED:
          return "stopped";
        case LiveStopStatus.RUNTIME_LIVE_STOP_STATUS_IDLE:
          return "idle";
        case LiveStopStatus.RUNTIME_LIVE_STOP_STATUS_UNSPECIFIED:
          throw new ContractError("Runtime Stop response returned an invalid status.");
      }
    },
    async forkEdit(target, input) {
      const requestedSessionID = requireChatSessionID(target);
      if (!nonBlank.safeParse(input.rollbackTargetID).success) {
        throw new TypeError("Rollback target ID is required.");
      }
      const method = SessionLifecycleService.method.resolveTransition;
      const call = await transport.callDescriptorAttachedProject({
        projectID: target.projectID,
        selector: target.workspace,
        method,
        createRequest: () =>
          create(method.input, {
            sessionId: requestedSessionID,
            transition: {
              action: SessionTransitionAction.FORK_ROLLBACK,
              forkRollbackTargetId: input.rollbackTargetID,
              initialInput: input.initialInput,
            },
          }),
      });
      requireProjectAttachment(call.attachment, target);
      const directive = requireChatSuccess(method, call.result);
      if (
        directive.directive.case !== "launch" ||
        directive.directive.value.intent?.intent.case !== "openExistingSessionId" ||
        !nonBlank.safeParse(directive.directive.value.intent.intent.value).success
      ) {
        throw new ContractError("Session Edit response did not identify the child Session.");
      }
      return directive.directive.value.intent.intent.value;
    },
    async listPendingWork(target) {
      const requestedSessionID = requireChatSessionID(target);
      const method = TurnService.method.listPendingWork;
      const call = await transport.callDescriptorAttachedProject({
        projectID: target.projectID,
        selector: target.workspace,
        method,
        createRequest: () => create(method.input, { sessionId: requestedSessionID }),
      });
      requireProjectAttachment(call.attachment, target);
      const success = requireChatSuccess(method, call.result);
      if (success.pendingWork === undefined) {
        throw new ContractError("Pending Work list response omitted its collection.");
      }
      return pendingWorkFromWire(success.pendingWork);
    },
    async removePendingWork(target, itemID) {
      const requestedSessionID = requireChatSessionID(target);
      const method = TurnService.method.removePendingWork;
      const call = await transport.callDescriptorAttachedProject({
        projectID: target.projectID,
        selector: target.workspace,
        method,
        createRequest: () =>
          create(method.input, {
            sessionId: requestedSessionID,
            itemId: itemID.toJSONValue(),
          }),
      });
      requireProjectAttachment(call.attachment, target);
      const success = requireChatSuccess(method, call.result);
      if (success.restoration === undefined) {
        throw new ContractError("Pending Work removal response omitted its restoration.");
      }
      return pendingWorkRestorationSchema.parse({
        kind: pendingWorkKind(success.restoration.kind),
        canonical_input: success.restoration.canonicalInput,
      });
    },
  };
}

function mutationTarget(
  target: ChatMutationTarget,
  workspaceID: string,
): {
  target:
    | { case: "session"; value: { sessionId: string } }
    | {
        case: "newChat";
        value: {
          projectId: string;
          workspaceId: string;
          initialSettings: {
            agentRole: string;
            supervisor: SupervisorValue;
            thinking?: string;
            fast?: boolean;
            questionsEnabled: boolean;
            autoCompactionEnabled: boolean;
          };
        };
      };
} {
  requireChatProjectTarget(target);
  if (target.kind === "session") {
    return { target: { case: "session", value: { sessionId: requireChatSessionID(target) } } };
  }
  return {
    target: {
      case: "newChat",
      value: {
        projectId: target.projectID,
        workspaceId: workspaceID,
        initialSettings: {
          agentRole: target.initialSettings.agentRole,
          supervisor: supervisorToWire(target.initialSettings.supervisor),
          ...(target.initialSettings.thinking === null ? {} : { thinking: target.initialSettings.thinking }),
          ...(target.initialSettings.fast === null ? {} : { fast: target.initialSettings.fast }),
          questionsEnabled: target.initialSettings.questionsEnabled,
          autoCompactionEnabled: target.initialSettings.autoCompactionEnabled,
        },
      },
    },
  };
}

function mutationSessionID(
  target: ChatMutationTarget,
  returnedSessionID: string | undefined,
  operation: string,
): string {
  if (
    returnedSessionID === undefined ||
    (target.kind === "session" && returnedSessionID !== target.sessionID)
  ) {
    throw new ContractError(`${operation} response Session does not match the request.`);
  }
  return returnedSessionID;
}

function chatActivation(activation: ChatActivation) {
  return activation.kind === "text"
    ? { input: { case: "text" as const, value: activation.text } }
    : {
        input: {
          case: "command" as const,
          value: {
            catalogIdentity: activation.catalogIdentity,
            token: activation.token,
            separatorWhitespace: activation.separatorWhitespace,
            arguments: activation.arguments,
          },
        },
      };
}

function acceptedDiagnostic(diagnostic: AcceptedDiagnostic | undefined) {
  if (diagnostic === undefined) return null;
  switch (diagnostic.detail.case) {
    case "promptHistoryFailure":
      return {
        kind: "prompt_history_failure" as const,
        operation: diagnostic.detail.value.operation ?? null,
        cause: diagnostic.detail.value.cause ?? null,
      };
    case "internalFailure":
      return {
        kind: "internal_failure" as const,
        operation: diagnostic.detail.value.operation ?? null,
        cause: diagnostic.detail.value.cause ?? null,
      };
    case undefined:
      throw new ContractError("Chat mutation accepted diagnostic returned no detail.");
  }
}

function internalFailure(value: { operation?: string | undefined; cause?: string | undefined }) {
  return {
    kind: "internal_failure" as const,
    operation: value.operation ?? null,
    cause: value.cause ?? null,
  };
}

function inputNotAccepted(reason: InputNotAccepted["reason"]) {
  switch (reason.case) {
    case "canceled":
      return { kind: "canceled" as const };
    case "runtimeUnavailable":
      return { kind: "runtime_unavailable" as const };
    case "pendingWorkCapacity":
      return { kind: "pending_work_capacity" as const };
    case "promptCatalogRead":
      return { kind: "prompt_catalog_read" as const, command: reason.value.command ?? null };
    case "promptCommandNotFound":
      return { kind: "prompt_command_not_found" as const, command: reason.value.command };
    case "promptCommandRead":
      return { kind: "prompt_command_read" as const, command: reason.value.command };
    case "internalFailure":
      return internalFailure(reason.value);
    case undefined:
      throw new ContractError("Chat mutation non-acceptance returned no reason.");
  }
}

function compactionNotAccepted(reason: CompactionNotAccepted["reason"]) {
  switch (reason.case) {
    case "canceled":
      return { kind: "canceled" as const };
    case "runtimeUnavailable":
      return { kind: "runtime_unavailable" as const };
    case "pendingWorkCapacity":
      return { kind: "pending_work_capacity" as const };
    case "tooSoon":
      return { kind: "too_soon" as const };
    case "disabled":
      return { kind: "disabled" as const };
    case "active":
      return { kind: "active" as const };
    case "internalFailure":
      return internalFailure(reason.value);
    case undefined:
      throw new ContractError("Chat compaction non-acceptance returned no reason.");
  }
}

function pendingWorkFromWire(input: WirePendingWork) {
  return pendingWorkSchema.parse({
    items: input.items.map((item) => {
      const shared = {
        id: item.id,
        lane: pendingWorkLane(item.lane),
        kind: pendingWorkKind(item.kind),
        state: pendingWorkState(item.state),
        canonical_input: item.canonicalInput,
      };
      switch (item.payload.case) {
        case "message":
          return { ...shared, message: { text: item.payload.value.text } };
        case "manualCompaction":
          return {
            ...shared,
            manual_compaction:
              item.payload.value.guidance === undefined ? {} : { guidance: item.payload.value.guidance },
          };
        case "worktreeTransition":
          return {
            ...shared,
            worktree_transition: {
              transition: pendingWorkTransition(item.payload.value.transition),
              ...(item.payload.value.selector === undefined ? {} : { selector: item.payload.value.selector }),
            },
          };
        case undefined:
          throw new ContractError("Pending Work item returned no payload.");
      }
    }),
  });
}

function pendingWorkLane(value: PendingWorkLane): "steer" | "queue" {
  switch (value) {
    case PendingWorkLane.STEER:
      return "steer";
    case PendingWorkLane.QUEUE:
      return "queue";
    case PendingWorkLane.UNSPECIFIED:
      throw new ContractError("Pending Work item returned an invalid lane.");
  }
}

function pendingWorkKind(value: PendingWorkItemKind) {
  switch (value) {
    case PendingWorkItemKind.MESSAGE:
      return "message" as const;
    case PendingWorkItemKind.MANUAL_COMPACTION:
      return "manual_compaction" as const;
    case PendingWorkItemKind.WORKTREE_TRANSITION:
      return "worktree_transition" as const;
    case PendingWorkItemKind.UNSPECIFIED:
      throw new ContractError("Pending Work item returned an invalid kind.");
  }
}

function pendingWorkState(value: PendingWorkItemState): "pending" {
  switch (value) {
    case PendingWorkItemState.PENDING:
      return "pending";
    case PendingWorkItemState.UNSPECIFIED:
      throw new ContractError("Pending Work item returned an invalid state.");
  }
}

function pendingWorkTransition(value: PendingWorkWorktreeTransitionKind): "enter" | "leave" {
  switch (value) {
    case PendingWorkWorktreeTransitionKind.ENTER:
      return "enter";
    case PendingWorkWorktreeTransitionKind.LEAVE:
      return "leave";
    case PendingWorkWorktreeTransitionKind.UNSPECIFIED:
      throw new ContractError("Pending Work item returned an invalid Worktree transition.");
  }
}
