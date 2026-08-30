import { z } from "zod";

import { activateRuntime } from "./chatActivation";
import { ContractError } from "./errors";
import { parseRpcResponse } from "./clientParse";
import {
  committedRowSchema,
  contextSchema,
  mainViewSchema,
  nonBlank,
  pageSchema,
  settingsSchema,
} from "./chatSchemas";
import type { executionTargetSchema, runtimeActivitySchema, runtimeStatusSchema } from "./chatSchemas";
import { transcriptEventSchema } from "./chatTranscriptSchemas";
import { requireProjectAttachment } from "./chatAttachment";
import { requireSessionAttachment } from "./jsonRpcSocket";
import type {
  ChatApi,
  ChatContextTarget,
  ChatMainView,
  ChatProjectTarget,
  ChatRuntimeActivity,
  ChatRuntimeStatus,
  ChatSessionTarget,
  ChatSettings,
  ChatSettingsTarget,
  ChatTranscriptMessage,
} from "./chatTypes";
export type {
  ChatApi,
  ChatContext,
  ChatContextTarget,
  ChatExecutionTarget,
  ChatMainView,
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
function projectTarget(target: ChatProjectTarget): void {
  if (!nonBlank.safeParse(target.projectID).success) throw new TypeError("Project ID is required.");
  const selector =
    "workspaceID" in target.workspace ? target.workspace.workspaceID : target.workspace.workspaceRoot;
  if (!nonBlank.safeParse(selector).success) throw new TypeError("Workspace selector is required.");
}
function contextTarget(target: ChatContextTarget): void {
  projectTarget(target);
  if (target.sessionID !== undefined && !nonBlank.safeParse(target.sessionID).success) {
    throw new TypeError("Session ID is required.");
  }
}
function sessionTarget(target: ChatSessionTarget): void {
  projectTarget(target);
  if (!nonBlank.safeParse(target.sessionID).success) throw new TypeError("Session ID is required.");
}
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
function settingsFromWire(input: z.output<typeof settingsSchema>, target: ChatSettingsTarget): ChatSettings {
  const isSession = target.kind === "session";
  if (isSession !== (input.session !== undefined))
    throw new ContractError("Chat Settings response target kind does not match the request.");
  if (isSession && input.session?.session_id !== target.sessionID)
    throw new ContractError("Chat Settings response Session does not match the request.");
  return {
    selectedAgent: {
      role: input.settings.selected_agent.role,
      model: input.settings.selected_agent.model,
      thinking: input.settings.selected_agent.thinking,
    },
    agentChoices: input.settings.agent_choices.map((choice) => ({
      role: choice.role,
      model: choice.model,
      thinking: choice.thinking,
      tools: choice.tools,
      customSystemPrompt: choice.custom_system_prompt,
      customCapabilities: choice.custom_capabilities,
      agentCallable: choice.agent_callable,
    })),
    agentEditability: input.settings.agent_editability,
    supervisor: input.settings.supervisor,
    thinking:
      input.settings.thinking === undefined || input.settings.thinking === null
        ? null
        : {
            kind: input.settings.thinking.kind,
            value: input.settings.thinking.value,
            baselineValue: input.settings.thinking.baseline_value,
            values: input.settings.thinking.values ?? [],
            editability: input.settings.thinking.editability,
          },
    fast: input.settings.fast ?? null,
    questions: input.settings.questions,
    autoCompaction: input.settings.auto_compaction,
    agentLocked: input.settings.agent_locked,
    workflowLocked: input.settings.workflow_locked,
    cachingLocked: input.settings.caching_locked,
    session:
      input.session === undefined
        ? null
        : {
            sessionID: input.session.session_id,
            previousSessionID: input.session.previous_session_id ?? null,
            taskID: input.session.task_id ?? null,
          },
  };
}

export function createChatApi(transport: DescriptorRpcTransport): ChatApi {
  return {
    async getMainView(target) {
      sessionTarget(target);
      const call = await transport.callAttachedProject({
        projectID: target.projectID,
        selector: target.workspace,
        method: "session.getMainView",
        request: { kind: "value", value: { SessionID: target.sessionID } },
      });
      requireProjectAttachment(call.attachment, target);
      const response = parseRpcResponse("session.getMainView", mainViewSchema, call.result);
      if (response.MainView.Session.SessionID !== target.sessionID)
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
      contextTarget(target);
      const call = await transport.callAttachedProject({
        projectID: target.projectID,
        selector: target.workspace,
        method: "chat.context.get",
        request: {
          kind: "value",
          value:
            target.sessionID === undefined
              ? { target: { workspace_chat: {} } }
              : { target: { session: { session_id: target.sessionID } } },
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
    async getSettings(target) {
      if (target.kind === "session") sessionTarget(target);
      else projectTarget(target);
      const call = await transport.callAttachedProject({
        projectID: target.projectID,
        selector: target.workspace,
        method: "chat.settings.read",
        request: {
          kind: "factory",
          create: (attachment) =>
            target.kind === "lazy"
              ? {
                  target: {
                    kind: "lazy",
                    project_id: attachment.projectID,
                    workspace_id: attachment.workspaceID,
                  },
                }
              : { target: { kind: "session", session_id: target.sessionID } },
        },
      });
      requireProjectAttachment(call.attachment, target);
      return settingsFromWire(parseRpcResponse("chat.settings.read", settingsSchema, call.result), target);
    },
    async getTranscriptPage(target, cursor) {
      sessionTarget(target);
      const call = await transport.callAttachedProject({
        projectID: target.projectID,
        selector: target.workspace,
        method: "session.getTranscriptPage",
        request: {
          kind: "value",
          value: {
            session_id: target.sessionID,
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
      if (value.SessionID !== target.sessionID)
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
      sessionTarget(target);
      return transport.runRuntimeOwner(target.sessionID, { createIfMissing: true }, async (owner) => {
        requireSessionAttachment(owner.attachment, target);
        return activateRuntime(owner, target.sessionID);
      });
    },
    async releaseRuntime(attachment) {
      if (
        !nonBlank.safeParse(attachment.sessionID).success ||
        !Number.isInteger(attachment.generation) ||
        attachment.generation <= 0
      )
        throw new TypeError("Runtime attachment is invalid.");
      return transport.runRuntimeOwner(
        attachment.sessionID,
        { createIfMissing: false, closeAfter: true },
        async (owner) => {
          requireSessionAttachment(owner.attachment, { sessionID: attachment.sessionID });
          const result = parseRpcResponse(
            "session.runtime.release",
            z.object({ released: z.boolean(), active: z.boolean().optional() }).strict(),
            await owner.call("session.runtime.release", {
              attachment: { session_id: attachment.sessionID, generation: attachment.generation },
              drop_owner: true,
              close_policy: "close_if_idle",
            }),
          );
          return { released: result.released, active: result.active ?? false };
        },
      );
    },
    subscribeTranscript(target, handler) {
      sessionTarget(target);
      const rpcHandler: RpcEventHandler = {
        ...(handler.onOpen === undefined ? {} : { onOpen: handler.onOpen }),
        onEvent(method, params) {
          if (method !== "session.transcript")
            throw new ContractError("Transcript subscription received an unexpected event.");
          let event: ChatTranscriptMessage;
          try {
            event = transcriptMessageFromTarget(
              parseRpcResponse("session.transcript", transcriptEventSchema, params).message,
              target.sessionID,
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
          handler.onError(error.contractError);
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
        sessionID: target.sessionID,
        method: "session.subscribeTranscript",
        params: { SessionID: target.sessionID },
        handler: rpcHandler,
      });
    },
  };
}
