import { z } from "zod";

import { activateRuntime } from "./chatActivation";
import { createChatMutationApi } from "./chatMutations";
import { createChatSettingsApi } from "./chatSettings";
import { ContractError } from "./errors";
import { parseRpcResponse } from "./clientParse";
import { committedRowSchema, contextSchema, mainViewSchema, pageSchema } from "./chatSchemas";
import type { executionTargetSchema, runtimeActivitySchema, runtimeStatusSchema } from "./chatSchemas";
import { transcriptEventSchema } from "./chatTranscriptSchemas";
import { requireProjectAttachment } from "./chatAttachment";
import { requireSessionAttachment } from "./jsonRpcSocket";
import { SubscriptionErrorAlreadyReported } from "./jsonRpcSubscription";
import { chatContextSessionID, isValidChatSessionID, requireChatSessionID } from "./chatTarget";
import type {
  ChatApi,
  ChatMainView,
  ChatRuntimeActivity,
  ChatRuntimeStatus,
  ChatTranscriptMessage,
} from "./chatTypes";
export type {
  ChatApi,
  ChatAcceptedDiagnostic,
  ChatCompactionInvocation,
  ChatCompactionResult,
  ChatContext,
  ChatContextTarget,
  ChatExecutionTarget,
  ChatForkEditInput,
  InitialChatSettings,
  ChatInputMutationResult,
  ChatMainView,
  ChatActivation,
  ChatMutationTarget,
  ChatNotAcceptedReason,
  ChatProjectTarget,
  ChatRuntimeActivity,
  ChatRuntimeAttachment,
  ChatRuntimeRelease,
  ChatRuntimeStatus,
  ChatSessionTarget,
  ChatSettings,
  ChatSettingsTarget,
  ChatTranscriptCompletion,
  ChatTranscriptCommittedRow,
  ChatTranscriptHandler,
  ChatTranscriptKind,
  ChatTranscriptMessage,
  ChatTranscriptMessageByKind,
  ChatTranscriptPage,
  ChatTranscriptPayload,
  ChatTranscriptPayloadByKind,
  ChatWorkspaceSelector,
} from "./chatTypes";
import type { RpcEventHandler, DescriptorRpcTransport } from "./transport";
class RecoverableTranscriptEventError extends Error {
  constructor(readonly contractError: ContractError) {
    super(contractError.message);
  }
}
function transcriptMessageFromTarget(input: ChatTranscriptMessage, sessionID: string): ChatTranscriptMessage {
  if (input.kind === "session_identity") {
    if (input.payload.SessionID !== sessionID) {
      throw new RecoverableTranscriptEventError(
        new ContractError("Transcript event Session identity does not match the requested Session."),
      );
    }
  }
  if (input.kind === "hydration") {
    if (input.payload.SessionIdentity.SessionID !== sessionID) {
      throw new RecoverableTranscriptEventError(
        new ContractError("Transcript hydration Session identity does not match the requested Session."),
      );
    }
  }
  return input;
}
function executionTarget(input: z.output<typeof executionTargetSchema>): ChatMainView["executionTarget"] {
  return {
    workspaceID: input.WorkspaceID,
    workspaceName: input.WorkspaceName,
    workspaceRoot: input.WorkspaceRoot,
    workspaceAvailability: input.WorkspaceAvailability,
    worktree: input.Worktree,
    cwdRelpath: input.CwdRelpath,
    effectiveWorkdir: input.EffectiveWorkdir,
  };
}
function runtimeStatus(input: z.output<typeof runtimeStatusSchema>): ChatRuntimeStatus {
  return {
    reviewerFrequency: input.ReviewerFrequency,
    reviewerEnabled: input.ReviewerEnabled,
    autoCompactionEnabled: input.AutoCompactionEnabled,
    questionsEnabled: input.QuestionsEnabled,
    fastModeAvailable: input.FastModeAvailable,
    fastModeEnabled: input.FastModeEnabled,
    conversationFreshness: input.ConversationFreshness,
    previousSessionID: input.PreviousSessionID ?? null,
    parentAgentSessionID: input.ParentAgentSessionID ?? null,
    navigationTargetSessionID: input.NavigationTargetSessionID ?? null,
    lastCommittedAssistantFinalAnswer: input.LastCommittedAssistantFinalAnswer ?? null,
    thinkingLevel: input.ThinkingLevel,
    compactionMode: input.CompactionMode,
    contextUsage: {
      usedTokens: input.ContextUsage.UsedTokens,
      windowTokens: input.ContextUsage.WindowTokens,
      cacheHitPercent: input.ContextUsage.CacheHitPercent,
      hasCacheHitPercentage: input.ContextUsage.HasCacheHitPercentage,
    },
    compactionCount: input.CompactionCount,
    goal:
      input.Goal === null
        ? null
        : {
            id: input.Goal.id,
            objective: input.Goal.objective,
            status: input.Goal.status,
            created_at: input.Goal.created_at,
            updated_at: input.Goal.updated_at,
            availability: input.Goal.Availability,
            suspended: input.Goal.Suspended,
          },
    workflowSession:
      input.WorkflowSession === null
        ? null
        : { taskID: input.WorkflowSession.TaskID, workflowID: input.WorkflowSession.WorkflowID },
  };
}
function runtimeActivity(input: z.output<typeof runtimeActivitySchema>): ChatRuntimeActivity {
  return {
    state: input.State,
    activeStep:
      input.ActiveStep === null
        ? null
        : {
            runID: input.ActiveStep.RunID,
            stepID: input.ActiveStep.StepID,
            activeKind: input.ActiveStep.ActiveKind,
          },
    reviewer: input.Reviewer,
    queueAccepting: input.QueueAccepting,
    diagnosticRecovery: input.DiagnosticRecovery,
  };
}
export function createChatApi(transport: DescriptorRpcTransport): ChatApi {
  return {
    ...createChatMutationApi(transport),
    ...createChatSettingsApi(transport),
    async getMainView(target) {
      const requestedSessionID = requireChatSessionID(target);
      const call = await transport.callAttachedProject({
        projectID: target.projectID,
        selector: target.workspace,
        method: "session.getMainView",
        request: { kind: "value", value: { SessionID: requestedSessionID } },
      });
      requireProjectAttachment(call.attachment, target);
      const response = parseRpcResponse("session.getMainView", mainViewSchema, call.result);
      if (response.MainView.Session.SessionID !== requestedSessionID)
        throw new ContractError("Session Main View does not match the requested Session.");
      return {
        version: {
          epoch: response.MainView.Version.Epoch,
          generation: response.MainView.Version.Generation,
          sequence: response.MainView.Version.Sequence,
        },
        status: runtimeStatus(response.MainView.Status),
        sessionID: response.MainView.Session.SessionID,
        sessionName:
          response.MainView.Session.SessionName === "" ? null : response.MainView.Session.SessionName,
        executionTarget: executionTarget(response.MainView.Session.ExecutionTarget),
        activity: runtimeActivity(response.MainView.Activity),
      };
    },
    async getContext(target) {
      const requestedSessionID = chatContextSessionID(target);
      const call = await transport.callAttachedProject({
        projectID: target.projectID,
        selector: target.workspace,
        method: "chat.context.get",
        request: {
          kind: "value",
          value:
            requestedSessionID === undefined
              ? { target: { workspace_chat: {} } }
              : { target: { session: { session_id: requestedSessionID } } },
        },
      });
      requireProjectAttachment(call.attachment, target);
      const value = parseRpcResponse("chat.context.get", contextSchema, call.result).context;
      return {
        contextWindowTokens: value.context_window_tokens,
        usedTokens: value.used_tokens,
        remainingTokens: value.remaining_tokens,
        automaticThresholdTokens: value.automatic_threshold_tokens,
        autoCompactionEnabled: value.auto_compaction_enabled,
        compactionMode: value.compaction_mode,
        completedCompactionCount: value.completed_compaction_count,
        compactionRunning: value.compaction_running,
        manualCompactAvailable: value.manual_compact_available,
      };
    },
    async getTranscriptPage(target, cursor) {
      const requestedSessionID = requireChatSessionID(target);
      const call = await transport.callAttachedProject({
        projectID: target.projectID,
        selector: target.workspace,
        method: "session.getTranscriptPage",
        request: {
          kind: "value",
          value: {
            session_id: requestedSessionID,
            ...(cursor === undefined
              ? {}
              : cursor.direction === "older"
                ? { cursor: cursor.value }
                : { newer_cursor: cursor.value }),
          },
        },
      });
      requireProjectAttachment(call.attachment, target);
      const value = parseRpcResponse("session.getTranscriptPage", pageSchema, call.result).transcript;
      if (value.SessionID !== requestedSessionID)
        throw new ContractError("Transcript page does not match the requested Session.");
      return {
        sessionID: value.SessionID,
        sessionName: value.SessionName === "" ? null : value.SessionName,
        conversationFreshness: value.ConversationFreshness,
        olderCursor: value.OlderCursor ?? null,
        hasMoreAbove: value.HasMoreAbove,
        newerCursor: value.NewerCursor ?? null,
        hasMoreBelow: value.HasMoreBelow,
        latestRollbackCandidate: value.LatestRollbackCandidate ?? null,
        entries: value.Entries.map((entry) =>
          parseRpcResponse("session.getTranscriptPage", committedRowSchema, entry),
        ),
      };
    },
    async activateRuntime(target) {
      const requestedSessionID = requireChatSessionID(target);
      return transport.runRuntimeOwner(requestedSessionID, { createIfMissing: true }, async (owner) => {
        requireSessionAttachment(owner.attachment, {
          projectID: target.projectID,
          sessionID: requestedSessionID,
        });
        return activateRuntime(owner, requestedSessionID);
      });
    },
    async releaseRuntime(attachment) {
      if (
        !isValidChatSessionID(attachment.sessionID) ||
        !Number.isInteger(attachment.generation) ||
        attachment.generation <= 0
      )
        throw new TypeError("Runtime attachment is invalid.");
      const requestedSessionID = attachment.sessionID;
      return transport.runRuntimeOwner(
        requestedSessionID,
        { createIfMissing: false, closeAfter: true },
        async (owner) => {
          requireSessionAttachment(owner.attachment, { sessionID: requestedSessionID });
          const result = parseRpcResponse(
            "session.runtime.release",
            z.object({ released: z.boolean(), active: z.boolean().optional() }).strict(),
            await owner.call("session.runtime.release", {
              attachment: { session_id: requestedSessionID, generation: attachment.generation },
              drop_owner: true,
              close_policy: "close_if_idle",
            }),
          );
          return { released: result.released, active: result.active ?? false };
        },
      );
    },
    subscribeTranscript(target, handler) {
      const requestedSessionID = requireChatSessionID(target);
      const rpcHandler: RpcEventHandler = {
        ...(handler.onOpen === undefined ? {} : { onOpen: handler.onOpen }),
        onEvent(method, params) {
          if (method !== "session.transcript")
            throw new ContractError("Transcript subscription received an unexpected event.");
          let event: ChatTranscriptMessage;
          try {
            event = transcriptMessageFromTarget(
              parseRpcResponse("session.transcript", transcriptEventSchema, params).message,
              requestedSessionID,
            );
          } catch (error) {
            if (error instanceof RecoverableTranscriptEventError) throw error;
            if (error instanceof ContractError)
              throw new RecoverableTranscriptEventError(new ContractError("Transcript event is invalid."));
            throw error;
          }
          handler.onEvent(event);
        },
        onEventFailure(error) {
          if (!(error instanceof RecoverableTranscriptEventError)) return false;
          try {
            handler.onError(error.contractError);
          } catch (cause) {
            throw new SubscriptionErrorAlreadyReported(
              cause instanceof Error ? cause : new ContractError("Subscription error handler failed."),
            );
          }
          return true;
        },
        onComplete(code, message, reason) {
          handler.onComplete({
            code,
            message,
            reason: reason === "subscriber_overflow" || reason === "contract_violation" ? reason : null,
          });
        },
        onError: handler.onError,
      };
      return transport.subscribeChatSession({
        projectID: target.projectID,
        sessionID: requestedSessionID,
        method: "session.subscribeTranscript",
        params: { SessionID: requestedSessionID },
        handler: rpcHandler,
      });
    },
  };
}
