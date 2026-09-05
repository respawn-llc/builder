import type { DescMethod, Message, MessageShape } from "@app/server-api-contract";
import type { ChatOperationError as WireChatOperationError } from "@app/server-api-contract/gen/kent/api/chat/chat_pb";
import { AgentPreparationCategory } from "@app/server-api-contract/gen/kent/api/chat_settings/chat_settings_pb";
import type {
  ListPendingWorkError,
  LiveStopError,
  RemovePendingWorkError,
} from "@app/server-api-contract/gen/kent/api/runtime/runtime_pb";
import type { SessionResolveTransitionError } from "@app/server-api-contract/gen/kent/api/session_launch/session_lifecycle_pb";

import { ContractError, RpcError } from "./errors";
import { protobufRpcError } from "./protobufRpc";

export type ChatError =
  | Readonly<{ kind: "session_not_found"; sessionID: string }>
  | Readonly<{ kind: "workspace_not_registered" }>
  | Readonly<{
      kind: "agent_preparation";
      agent: string;
      category: "invalid_configuration" | "provider_unavailable" | "internal_preparation";
    }>
  | Readonly<{ kind: "auth_required" }>
  | Readonly<{ kind: "server_not_ready" }>
  | Readonly<{ kind: "runtime_unavailable"; sessionID: string }>
  | Readonly<{ kind: "internal_failure"; operation: string | null; cause: string | null }>
  | Readonly<{ kind: "unknown"; code: string }>;

export class ChatOperationError extends RpcError {
  constructor(
    rpcError: RpcError,
    readonly detail: ChatError,
  ) {
    super(rpcError);
    this.name = "ChatOperationError";
  }
}

type ChatWireError =
  | WireChatOperationError
  | ListPendingWorkError
  | LiveStopError
  | RemovePendingWorkError
  | SessionResolveTransitionError;

type ChatRpcResult = Readonly<{
  outcome:
    | Readonly<{ case: "success"; value: Message }>
    | Readonly<{ case: "error"; value: ChatWireError }>
    | Readonly<{ case: undefined; value?: undefined }>;
}>;
type MethodOutcome<Method extends DescMethod> =
  MessageShape<Method["output"]> extends Readonly<{ outcome: infer Outcome }> ? Outcome : never;
type MethodSuccess<Method extends DescMethod> =
  Extract<MethodOutcome<Method>, Readonly<{ case: "success" }>> extends Readonly<{
    value: infer Success;
  }>
    ? Success
    : never;

export function requireChatSuccess<Method extends DescMethod>(
  method: Method,
  result: ChatRpcResult,
): MethodSuccess<Method>;
export function requireChatSuccess(method: DescMethod, result: ChatRpcResult): Message {
  switch (result.outcome.case) {
    case "success":
      return result.outcome.value;
    case "error":
      throw chatOperationError(method, result.outcome.value);
    case undefined:
      throw new ContractError("Chat operation returned no outcome.");
  }
}

export function chatOperationError(method: DescMethod, failure: ChatWireError): ChatOperationError {
  const generic = protobufRpcError(method, failure);
  switch (failure.detail.case) {
    case "sessionNotFound":
      return new ChatOperationError(generic, {
        kind: "session_not_found",
        sessionID: failure.detail.value.sessionId,
      });
    case "workspaceNotRegistered":
      return new ChatOperationError(generic, { kind: "workspace_not_registered" });
    case "chatSettingsAgentPreparation":
      return new ChatOperationError(generic, {
        kind: "agent_preparation",
        agent: failure.detail.value.agent,
        category: agentPreparationCategory(failure.detail.value.category),
      });
    case "authRequired":
      return new ChatOperationError(generic, { kind: "auth_required" });
    case "serverNotReady":
      return new ChatOperationError(generic, { kind: "server_not_ready" });
    case "runtimeUnavailable":
      return new ChatOperationError(generic, {
        kind: "runtime_unavailable",
        sessionID: failure.detail.value.sessionId,
      });
    case "internalFailure":
      return new ChatOperationError(generic, {
        kind: "internal_failure",
        operation: failure.detail.value.operation ?? null,
        cause: failure.detail.value.cause ?? null,
      });
    case undefined:
      if (failure.code.length === 0) {
        throw new ContractError("Chat operation returned an empty error code.");
      }
      return new ChatOperationError(generic, { kind: "unknown", code: failure.code });
  }
}

function agentPreparationCategory(
  category: AgentPreparationCategory,
): "invalid_configuration" | "provider_unavailable" | "internal_preparation" {
  switch (category) {
    case AgentPreparationCategory.INVALID_CONFIGURATION:
      return "invalid_configuration";
    case AgentPreparationCategory.PROVIDER_UNAVAILABLE:
      return "provider_unavailable";
    case AgentPreparationCategory.INTERNAL_PREPARATION:
      return "internal_preparation";
    case AgentPreparationCategory.UNSPECIFIED:
      throw new ContractError("Chat operation returned an invalid Agent preparation category.");
  }
}

export function chatErrorMessage(error: ChatError): string {
  switch (error.kind) {
    case "session_not_found":
      return "This Session no longer exists.";
    case "workspace_not_registered":
      return "This workspace is no longer registered.";
    case "agent_preparation":
      return `The ${error.agent} agent could not be prepared.`;
    case "auth_required":
      return "Sign in before starting this Chat.";
    case "server_not_ready":
      return "Kent is not ready to start this Chat yet.";
    case "runtime_unavailable":
      return "This Session is not available right now.";
    case "internal_failure":
      return "Kent could not complete the Chat operation.";
    case "unknown":
      return `Kent could not complete the Chat operation (${error.code}).`;
  }
}
