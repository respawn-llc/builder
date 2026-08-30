import { create, validate } from "@app/server-api-contract";
import { z } from "zod";
import {
  BackgroundShellOutputMode,
  CacheWarningMode,
  CompactionMode,
  ModelVerbosity,
  SessionLaunchMode,
  SessionLaunchService,
  SessionPlanRequestSchema,
  ShellPostprocessingMode,
  SleepPreventionMode,
  ToolID,
  WorkflowCompletionMode,
  type ReviewerSettings,
  type SessionPlan,
  type Settings,
  type SourceReport,
} from "@app/server-api-contract/gen/kent/api/session_launch/session_launch_pb";
import { ContractError } from "./errors";
import { requireUnarySuccess } from "./protobufRpc";
import type { JsonObject, JsonValue } from "./json";
import type { RuntimeOwnerContext } from "./transport";

export async function activateRuntime(
  owner: RuntimeOwnerContext,
  sessionID: string,
): Promise<Readonly<{ sessionID: string; generation: number }>> {
  const result = await owner.callDescriptor(
    SessionLaunchService.method.plan,
    create(SessionPlanRequestSchema, {
      mode: SessionLaunchMode.INTERACTIVE,
      intent: { intent: { case: "openExistingSessionId", value: sessionID } },
    }),
  );
  validate(SessionLaunchService.method.plan.output, result);
  const success = requireUnarySuccess(SessionLaunchService.method.plan, result);
  const plan = required(success.plan, "Plan");
  if (plan.sessionId !== sessionID) {
    throw new ContractError("Session Plan does not match the requested Session.");
  }
  let response: unknown;
  try {
    response = await owner.call("session.runtime.activate", activationRequest(plan));
  } catch (error) {
    owner.poison();
    throw error;
  }
  const decoded = activationResponseSchema.safeParse(response);
  if (!decoded.success || decoded.data.attachment.session_id !== sessionID) {
    owner.poison();
    throw new ContractError("Runtime activation does not match the requested Session.");
  }
  return {
    sessionID: decoded.data.attachment.session_id,
    generation: decoded.data.attachment.generation,
  };
}

const activationResponseSchema = z
  .object({
    attachment: z
      .object({ session_id: z.string().trim().min(1), generation: z.number().int().positive() })
      .strict(),
  })
  .strict();

function required<T>(value: T | undefined, field: string): T {
  if (value === undefined) throw new ContractError(`Session Plan is missing ${field}.`);
  return value;
}

const enumLabels = {
  modelVerbosity: {
    [ModelVerbosity.UNSPECIFIED]: "",
    [ModelVerbosity.LOW]: "low",
    [ModelVerbosity.MEDIUM]: "medium",
    [ModelVerbosity.HIGH]: "high",
  },
  compactionMode: {
    [CompactionMode.UNSPECIFIED]: "",
    [CompactionMode.NATIVE]: "native",
    [CompactionMode.LOCAL]: "local",
    [CompactionMode.NONE]: "none",
  },
  backgroundShellOutput: {
    [BackgroundShellOutputMode.UNSPECIFIED]: "",
    [BackgroundShellOutputMode.DEFAULT]: "default",
    [BackgroundShellOutputMode.VERBOSE]: "verbose",
    [BackgroundShellOutputMode.CONCISE]: "concise",
  },
  shellPostprocessing: {
    [ShellPostprocessingMode.UNSPECIFIED]: "",
    [ShellPostprocessingMode.NONE]: "none",
    [ShellPostprocessingMode.BUILTIN]: "builtin",
    [ShellPostprocessingMode.USER]: "user",
    [ShellPostprocessingMode.ALL]: "all",
  },
  cacheWarning: {
    [CacheWarningMode.UNSPECIFIED]: "",
    [CacheWarningMode.OFF]: "off",
    [CacheWarningMode.DEFAULT]: "default",
    [CacheWarningMode.VERBOSE]: "verbose",
  },
  workflowCompletion: {
    [WorkflowCompletionMode.UNSPECIFIED]: "",
    [WorkflowCompletionMode.AUTO]: "auto",
    [WorkflowCompletionMode.STRUCTURED_OUTPUT]: "structured_output",
    [WorkflowCompletionMode.TOOL]: "tool",
    [WorkflowCompletionMode.SHELL_COMMAND]: "shell_command",
    [WorkflowCompletionMode.UNSTRUCTURED_OUTPUT]: "unstructured_output",
  },
  sleepPrevention: {
    [SleepPreventionMode.UNSPECIFIED]: "",
    [SleepPreventionMode.ALWAYS]: "always",
    [SleepPreventionMode.ACTIVE]: "active",
    [SleepPreventionMode.NEVER]: "never",
  },
} as const;

function label(value: number, labels: Readonly<Record<number, string>>, field: string): string {
  const result = labels[value];
  if (result === undefined) throw new ContractError(`Session Plan contains an unsupported ${field}.`);
  return result;
}

function facts(values: readonly Readonly<{ key: string; value: JsonValue }>[], field: string): JsonObject {
  const result: Record<string, JsonValue> = {};
  for (const value of values) {
    if (Object.hasOwn(result, value.key))
      throw new ContractError(`Session Plan contains duplicate ${field} ${value.key}.`);
    result[value.key] = value.value;
  }
  return result;
}

function tool(value: ToolID): string {
  switch (value) {
    case ToolID.TOOL_ID_EXEC_COMMAND:
      return "exec_command";
    case ToolID.TOOL_ID_WRITE_STDIN:
      return "write_stdin";
    case ToolID.TOOL_ID_VIEW_IMAGE:
      return "view_image";
    case ToolID.TOOL_ID_PATCH:
      return "patch";
    case ToolID.TOOL_ID_EDIT:
      return "edit";
    case ToolID.TOOL_ID_ASK_QUESTION:
      return "ask_question";
    case ToolID.TOOL_ID_COMPLETE_NODE:
      return "complete_node";
    case ToolID.TOOL_ID_TRIGGER_HANDOFF:
      return "trigger_handoff";
    case ToolID.TOOL_ID_WEB_SEARCH:
      return "web_search";
    case ToolID.TOOL_ID_UNSPECIFIED:
      throw new ContractError("Session Plan contains an unspecified enabled tool.");
  }
}

function capabilities(value: NonNullable<Settings["modelCapabilities"]>): JsonObject {
  return {
    SupportsReasoningEffort: value.supportsReasoningEffort,
    SupportsVisionInputs: value.supportsVisionInputs,
  };
}

function provider(value: NonNullable<Settings["providerCapabilities"]>): JsonObject {
  if (value.supportsRequestInputTokenCount)
    throw new ContractError("Session Plan requires unsupported request input token count capability.");
  return {
    ProviderID: value.providerId,
    SupportsResponsesAPI: value.supportsResponsesApi,
    SupportsResponsesCompact: value.supportsResponsesCompact,
    SupportsPromptCacheKey: value.supportsPromptCacheKey,
    SupportsNativeWebSearch: value.supportsNativeWebSearch,
    SupportsReasoningEncrypted: value.supportsReasoningEncrypted,
    SupportsServerSideContextEdit: value.supportsServerSideContextEdit,
    SupportsProviderVerbosity: value.supportsProviderVerbosity,
    IsOpenAIFirstParty: value.isOpenaiFirstParty,
  };
}

function settings(value: Settings): JsonObject {
  const shell = required(value.shell, "shell settings");
  const worktrees = required(value.worktrees, "worktree settings");
  const workflow = required(value.workflow, "workflow settings");
  return {
    Model: value.model,
    ThinkingLevel: value.thinkingLevel,
    ModelVerbosity: label(value.modelVerbosity, enumLabels.modelVerbosity, "model verbosity"),
    SystemPromptFile: value.systemPromptFile,
    SystemPromptFiles: value.systemPromptFiles.map((file) => ({
      Path: file.path,
      Scope: label(
        file.scope,
        { 0: "", 1: "home_config", 2: "workspace_config", 3: "subagent" },
        "system prompt file scope",
      ),
    })),
    ModelCapabilities: capabilities(required(value.modelCapabilities, "model capabilities")),
    Theme: value.theme,
    NotificationMethod: value.notificationMethod,
    ToolPreambles: value.toolPreambles,
    PriorityRequestMode: value.priorityRequestMode,
    Debug: value.debug,
    ServerHost: value.serverHost,
    ServerPort: value.serverPort,
    WebSearch: value.webSearch,
    ProviderOverride: value.providerOverride,
    ProviderIdentifier: value.providerIdentifier,
    OpenAIBaseURL: value.openaiBaseUrl,
    ProviderCapabilities: provider(required(value.providerCapabilities, "provider capabilities")),
    Store: value.store,
    AllowNonCwdEdits: value.allowNonCwdEdits,
    ModelContextWindow: value.modelContextWindow,
    ContextCompactionThresholdTokens: value.contextCompactionThresholdTokens,
    PreSubmitCompactionLeadTokens: value.preSubmitCompactionLeadTokens,
    ShellOutputMaxChars: value.shellOutputMaxChars,
    MinimumExecToBgSeconds: value.minimumExecToBgSeconds,
    CompactionMode: label(value.compactionMode, enumLabels.compactionMode, "compaction mode"),
    EnabledTools: facts(
      value.enabledTools.map((entry) => ({ key: tool(entry.toolId), value: entry.enabled })),
      "enabled tool",
    ),
    SkillToggles: facts(
      value.skillToggles.map((entry) => ({ key: entry.key, value: entry.value })),
      "skill toggle",
    ),
    Timeouts: { ModelRequestSeconds: required(value.timeouts, "timeouts").modelRequestSeconds },
    BGShellsOutput: label(
      value.bgShellsOutput,
      enumLabels.backgroundShellOutput,
      "background shell output mode",
    ),
    Shell: {
      PostprocessingMode: label(
        shell.postprocessingMode,
        enumLabels.shellPostprocessing,
        "shell postprocessing mode",
      ),
      ...(shell.postprocessHook === undefined ? {} : { PostprocessHook: shell.postprocessHook }),
    },
    CacheWarningMode: label(value.cacheWarningMode, enumLabels.cacheWarning, "cache warning mode"),
    Worktrees: {
      BaseDir: worktrees.baseDir,
      SetupScript: worktrees.setupScript,
      SetupTimeoutSeconds: worktrees.setupTimeoutSeconds,
    },
    Workflow: {
      CompletionMode: label(
        workflow.completionMode,
        enumLabels.workflowCompletion,
        "workflow completion mode",
      ),
      Concurrency: workflow.concurrency,
      MaxInvalidCompletionAttempts: workflow.maxInvalidCompletionAttempts,
      ...(workflow.preCompactionTokens === undefined
        ? {}
        : { PreCompactionTokens: workflow.preCompactionTokens }),
      UseRequiredToolCalls: workflow.useRequiredToolCalls,
      Subagents: workflow.subagents,
    },
    Reviewer: reviewer(required(value.reviewer, "Reviewer settings")),
    Subagents: subagents(value.subagents),
    MaxSubagentDepth: value.maxSubagentDepth,
    PreventSleep: label(value.preventSleep, enumLabels.sleepPrevention, "sleep prevention mode"),
  };
}

function reviewer(value: ReviewerSettings): JsonObject {
  return {
    Frequency: value.frequency,
    Model: value.model,
    ThinkingLevel: value.thinkingLevel,
    ModelVerbosity: label(value.modelVerbosity, enumLabels.modelVerbosity, "Reviewer model verbosity"),
    ProviderOverride: value.providerOverride,
    OpenAIBaseURL: value.openaiBaseUrl,
    ModelCapabilities: capabilities(required(value.modelCapabilities, "Reviewer model capabilities")),
    ProviderCapabilities: provider(required(value.providerCapabilities, "Reviewer provider capabilities")),
    ModelContextWindow: value.modelContextWindow,
    Auth: value.auth,
    SystemPromptFile: value.systemPromptFile,
    TimeoutSeconds: value.timeoutSeconds,
    VerboseOutput: value.verboseOutput,
  };
}

function subagents(values: Settings["subagents"]): JsonObject {
  const result: Record<string, JsonValue> = {};
  for (const entry of values) {
    if (Object.hasOwn(result, entry.name))
      throw new ContractError(`Session Plan contains duplicate Subagent ${entry.name}.`);
    const role = required(entry.role, `Subagent ${entry.name}`);
    result[entry.name] = {
      Settings: settings(required(role.settings, "Subagent settings")),
      Sources: facts(role.sources, "Subagent source"),
      Description: role.description,
      AgentCallable: role.agentCallable,
      AgentCallableSet: role.agentCallableSet,
      WorkflowSubagent: role.workflowSubagent,
      WorkflowSubagentSet: role.workflowSubagentSet,
    };
  }
  return result;
}

function source(value: SourceReport): JsonObject {
  return {
    SettingsPath: value.settingsPath,
    SettingsFileExists: value.settingsFileExists,
    CreatedDefaultConfig: value.createdDefaultConfig,
    HomeSettingsPath: value.homeSettingsPath,
    HomeSettingsFileExists: value.homeSettingsFileExists,
    WorkspaceSettingsPath: value.workspaceSettingsPath,
    WorkspaceSettingsFileExists: value.workspaceSettingsFileExists,
    WorkspaceSettingsLayerEnabled: value.workspaceSettingsLayerEnabled,
    Sources: facts(value.sources, "source report source"),
  };
}

function activationRequest(plan: SessionPlan): JsonObject {
  const selection = plan.activationAgentSelection;
  const baseline =
    selection === undefined ? undefined : required(selection.baseline, "activation agent baseline");
  return {
    session_id: plan.sessionId,
    active_settings: settings(required(plan.activeSettings, "active settings")),
    enabled_tool_ids: uniqueTools(plan.enabledToolIds),
    questions_enabled: plan.questionsEnabled,
    auto_compaction_enabled: plan.autoCompactionEnabled,
    thinking_override_explicit: plan.thinkingOverrideExplicit,
    source: source(required(plan.source, "source report")),
    ...(selection === undefined
      ? {}
      : {
          agent_selection: {
            agent: selection.agent,
            baseline: {
              supervisor: required(baseline, "activation agent baseline").supervisor,
              thinking: required(baseline, "activation agent baseline").thinking,
              fast: required(baseline, "activation agent baseline").fast,
              questions: required(baseline, "activation agent baseline").questions,
              auto_compaction: required(baseline, "activation agent baseline").autoCompaction,
            },
          },
        }),
  };
}

function uniqueTools(values: readonly ToolID[]): string[] {
  const result: string[] = [];
  const seen = new Set<string>();
  for (const value of values) {
    const mapped = tool(value);
    if (seen.has(mapped)) throw new ContractError(`Session Plan contains duplicate enabled tool ${mapped}.`);
    seen.add(mapped);
    result.push(mapped);
  }
  return result;
}
