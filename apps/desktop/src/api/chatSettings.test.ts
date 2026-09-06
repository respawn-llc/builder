import { create } from "@app/server-api-contract";
import {
  AutoCompactionPolicy,
  ChatSettingsService,
  Editability,
  ReadResultSchema,
  MutationResponseSchema,
  MutationRejectionReason,
  MutationResultSchema,
  AgentPreparationCategory,
  ReadErrorSchema,
  SettingsSchema,
  SupervisorValue,
  ThinkingKind,
} from "@app/server-api-contract/gen/kent/api/chat_settings/chat_settings_pb";

import { FakeRpcTransport } from "@/test-support/api";
import { ApiClient } from "./client";
import { ChatOperationError } from "./chatErrors";
import type { ChatError } from "./chatErrors";
import { ContractError } from "./errors";
import type { ChatSettingsMutation } from "./chatSettingsTypes";
import { CompactionMode } from "@app/server-api-contract/gen/kent/api/chat_context/chat_context_pb";
import { ServerNotReadyReason } from "@app/server-api-contract/gen/kent/api/server/server_pb";
import { ChatService } from "@app/server-api-contract/gen/kent/api/chat/chat_pb";

const sessionID = "123e4567-e89b-42d3-a456-426614174000";
const projectTarget = { projectID: "project-1", workspace: { workspaceRoot: "/workspace" } } as const;
const newChatTarget = { ...projectTarget, kind: "new_chat" } as const;
const sessionTarget = { ...projectTarget, kind: "session", sessionID } as const;

function choice(role = "default") {
  return {
    agent: { role, model: "gpt-5", thinking: "medium", tools: ["ask_question"], agentCallable: true },
    baseline: {
      agentRole: role,
      supervisor: SupervisorValue.AFTER_EDITS,
      thinking: "medium",
      fast: false,
      questionsEnabled: true,
      autoCompactionEnabled: true,
    },
    supervisor: {
      value: SupervisorValue.AFTER_EDITS,
      baseline: SupervisorValue.AFTER_EDITS,
      editability: Editability.EDITABLE,
    },
    thinking: {
      kind: ThinkingKind.ENUMERATED,
      value: "medium",
      baselineValue: "medium",
      values: ["low", "medium", "high"],
      editability: Editability.EDITABLE,
    },
    fast: { value: false, editability: Editability.EDITABLE },
    questions: { capable: true, enabled: true, editability: Editability.EDITABLE },
    autoCompaction: {
      policy: AutoCompactionPolicy.OPTIONAL,
      stored: true,
      effective: true,
      editability: Editability.EDITABLE,
    },
  };
}

function settings() {
  const baseline = choice();
  return create(SettingsSchema, {
    selectedAgent: baseline.agent,
    agentChoices: [baseline.agent],
    agentEditability: Editability.EDITABLE,
    supervisor: baseline.supervisor,
    thinking: baseline.thinking,
    fast: baseline.fast,
    questions: baseline.questions,
    autoCompaction: baseline.autoCompaction,
  });
}

function mutationResponse() {
  return create(MutationResponseSchema, {
    outcome: {
      case: "success",
      value: {
        result: { outcome: { case: "applied", value: { changed: true } } },
        settings: settings(),
        session: { sessionId: sessionID },
        context: {
          contextWindowTokens: 100n,
          usedTokens: 20n,
          remainingTokens: 80n,
          automaticThresholdTokens: 80n,
          autoCompactionEnabled: true,
          compactionMode: CompactionMode.LOCAL,
          completedCompactionCount: 2n,
          compactionRunning: false,
          manualCompactAvailable: true,
        },
      },
    },
  });
}

describe("Chat Settings descriptor adapter", () => {
  it("reads the ordered New Chat catalog separately from the exact creation selection", async () => {
    const first = choice();
    const second = choice("reviewer");
    second.baseline.supervisor = SupervisorValue.OFF;
    second.supervisor.value = SupervisorValue.OFF;
    second.supervisor.baseline = SupervisorValue.OFF;
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatSettingsService.method.read,
        result: create(ReadResultSchema, {
          outcome: {
            case: "success",
            value: {
              target: {
                case: "newChat",
                value: { choices: [first, second], initialSettings: first.baseline },
              },
            },
          },
        }),
      },
    ]);

    const result = await new ApiClient(transport).chat.getSettings(newChatTarget);

    expect(result).toMatchObject({
      kind: "new_chat",
      catalog: { choices: [{ agent: { role: "default" } }, { agent: { role: "reviewer" } }] },
    });
    if (result.kind !== "new_chat") throw new Error("Expected New Chat.");
    expect(result.initialSettings).toEqual({
      agentRole: "default",
      supervisor: "edits",
      thinking: "medium",
      fast: false,
      questionsEnabled: true,
      autoCompactionEnabled: true,
    });
    expect(result.catalog.choices[1]?.baseline).toEqual({
      ...result.initialSettings,
      agentRole: "reviewer",
      supervisor: "off",
    });
    expect(transport.attachedProjectDescriptorCalls[0]?.request).toMatchObject({
      target: { case: "newChat", value: { projectId: "project-1", workspaceId: "workspace-1" } },
    });
  });

  it("reads an existing Session with paired Task facts and independent parent lineage", async () => {
    const taskID = "task-existing";
    const parentID = "323e4567-e89b-42d3-a456-426614174000";
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatSettingsService.method.read,
        result: create(ReadResultSchema, {
          outcome: {
            case: "success",
            value: {
              target: {
                case: "session",
                value: {
                  settings: settings(),
                  session: {
                    sessionId: sessionID,
                    previousSessionId: parentID,
                    taskId: taskID,
                    taskShortId: "KENT-416",
                  },
                },
              },
            },
          },
        }),
      },
    ]);
    await expect(new ApiClient(transport).chat.getSettings(sessionTarget)).resolves.toMatchObject({
      kind: "session",
      settings: {
        selectedAgent: { role: "default" },
        agentEditability: { kind: "editable" },
        thinking: { kind: "enumerated", values: ["low", "medium", "high"] },
        fast: { kind: "supported", value: false },
        questions: { capable: true, enabled: true },
        autoCompaction: { policy: "optional", stored: true, effective: true },
      },
      session: { sessionID, previousSessionID: parentID, task: { taskID, shortID: "KENT-416" } },
    });
    expect(transport.attachedProjectDescriptorCalls[0]?.request).toMatchObject({
      target: { case: "session", value: { sessionId: sessionID } },
    });
  });

  it.each<
    Readonly<{
      operation: ChatSettingsMutation;
      wire: Readonly<{ case: string; value: string | boolean | number }>;
    }>
  >([
    { operation: { kind: "agent", role: "reviewer" }, wire: { case: "agentRole", value: "reviewer" } },
    {
      operation: { kind: "supervisor", value: "off" },
      wire: { case: "supervisor", value: SupervisorValue.OFF },
    },
    {
      operation: { kind: "thinking", value: "  custom effort  " },
      wire: { case: "thinking", value: "  custom effort  " },
    },
    { operation: { kind: "fast", enabled: false }, wire: { case: "fastEnabled", value: false } },
    { operation: { kind: "questions", enabled: false }, wire: { case: "questionsEnabled", value: false } },
    {
      operation: { kind: "auto_compaction", enabled: false },
      wire: { case: "autoCompactionEnabled", value: false },
    },
  ])(
    "sends semantic $operation.kind activation and returns complete applied projections",
    async ({ operation, wire }) => {
      const transport = new FakeRpcTransport([
        {
          descriptor: ChatSettingsService.method.mutate,
          result: mutationResponse(),
        },
      ]);
      const result = await new ApiClient(transport).chat.mutateSettings(sessionTarget, operation);
      expect(transport.attachedProjectDescriptorCalls[0]?.request).toMatchObject({
        session: { sessionId: sessionID },
        operation: { operation: wire },
      });
      expect(result).toMatchObject({
        result: { kind: "applied", changed: true },
        settings: { selectedAgent: { role: "default" } },
        session: { sessionID, previousSessionID: null, task: null },
        context: {
          contextWindowTokens: 100,
          usedTokens: 20,
          remainingTokens: 80,
          compactionMode: "local",
          completedCompactionCount: 2,
        },
      });
      expect(transport.descriptorCalls).toHaveLength(1);
      expect(transport.attachedProjectCalls).toHaveLength(0);
    },
  );

  it.each([
    { wire: MutationRejectionReason.AGENT_LOCKED, reason: "agent_locked" },
    { wire: MutationRejectionReason.AGENT_UNAVAILABLE, reason: "agent_unavailable" },
    { wire: MutationRejectionReason.THINKING_UNAVAILABLE, reason: "thinking_unavailable" },
    { wire: MutationRejectionReason.FAST_UNAVAILABLE, reason: "fast_unavailable" },
    { wire: MutationRejectionReason.AUTO_COMPACTION_POLICY_LOCKED, reason: "auto_compaction_policy_locked" },
  ])(
    "returns $reason with complete authoritative Settings, Session, and Context",
    async ({ wire, reason }) => {
      const response = mutationResponse();
      if (response.outcome.case !== "success") throw new Error("Expected success envelope.");
      response.outcome.value.result = create(MutationResultSchema, {
        outcome: { case: "rejected", value: { reason: wire } },
      });
      const transport = new FakeRpcTransport([
        { descriptor: ChatSettingsService.method.mutate, result: response },
      ]);
      await expect(
        new ApiClient(transport).chat.mutateSettings(sessionTarget, { kind: "agent", role: "reviewer" }),
      ).resolves.toMatchObject({
        result: { kind: "rejected", reason },
        settings: { selectedAgent: { role: "default" } },
        session: { sessionID },
        context: { usedTokens: 20 },
      });
      expect(transport.descriptorCalls).toHaveLength(1);
    },
  );

  it("preserves Chat-domain failures for both Settings reads and mutations", async () => {
    const error = create(ReadErrorSchema, {
      code: "chat_settings_agent_preparation",
      detail: {
        case: "chatSettingsAgentPreparation",
        value: {
          agent: "reviewer",
          category: AgentPreparationCategory.PROVIDER_UNAVAILABLE,
        },
      },
    });
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatSettingsService.method.read,
        result: create(ReadResultSchema, { outcome: { case: "error", value: error } }),
      },
      {
        descriptor: ChatSettingsService.method.mutate,
        result: create(MutationResponseSchema, { outcome: { case: "error", value: error } }),
      },
    ]);
    const chat = new ApiClient(transport).chat;
    for (const operation of [
      async () => chat.getSettings(newChatTarget),
      async () => chat.getSettings(sessionTarget),
      async () => chat.mutateSettings(sessionTarget, { kind: "agent", role: "reviewer" }),
    ]) {
      const failure: unknown = await operation().catch((cause: unknown) => cause);
      expect(failure).toBeInstanceOf(ChatOperationError);
      expect(failure).toMatchObject({
        detail: {
          kind: "agent_preparation",
          agent: "reviewer",
          category: "provider_unavailable",
        },
      });
    }
  });

  it("rejects a New Chat catalog whose initial Agent has no baseline choice", async () => {
    const first = choice();
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatSettingsService.method.read,
        result: create(ReadResultSchema, {
          outcome: {
            case: "success",
            value: {
              target: {
                case: "newChat",
                value: {
                  choices: [first],
                  initialSettings: { ...first.baseline, agentRole: "missing" },
                },
              },
            },
          },
        }),
      },
    ]);
    await expect(new ApiClient(transport).chat.getSettings(newChatTarget)).rejects.toBeInstanceOf(Error);
  });

  it("rejects ambiguous duplicate Agent baselines in the New Chat catalog", async () => {
    const first = choice();
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatSettingsService.method.read,
        result: create(ReadResultSchema, {
          outcome: {
            case: "success",
            value: {
              target: {
                case: "newChat",
                value: { choices: [first, first], initialSettings: first.baseline },
              },
            },
          },
        }),
      },
    ]);
    await expect(new ApiClient(transport).chat.getSettings(newChatTarget)).rejects.toBeInstanceOf(Error);
  });

  it("rejects malformed Task identity even when its short ID is paired", async () => {
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatSettingsService.method.read,
        result: create(ReadResultSchema, {
          outcome: {
            case: "success",
            value: {
              target: {
                case: "session",
                value: {
                  settings: settings(),
                  session: { sessionId: sessionID, taskId: "invalid", taskShortId: "KENT-416" },
                },
              },
            },
          },
        }),
      },
    ]);
    await expect(new ApiClient(transport).chat.getSettings(sessionTarget)).rejects.toBeInstanceOf(Error);
  });

  it("decodes unsupported capabilities and locked policy without inventing creation values", async () => {
    const current = settings();
    if (current.questions === undefined || current.autoCompaction === undefined)
      throw new Error("Incomplete fixture.");
    current.thinking = undefined;
    current.fast = undefined;
    current.questions = { ...current.questions, capable: false, enabled: true };
    current.workflowLocked = true;
    current.agentLocked = true;
    current.agentEditability = Editability.WORKFLOW_LOCK;
    current.autoCompaction = {
      ...current.autoCompaction,
      policy: AutoCompactionPolicy.REQUIRED,
      stored: false,
      effective: true,
      editability: Editability.WORKFLOW_LOCK,
    };
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatSettingsService.method.read,
        result: create(ReadResultSchema, {
          outcome: {
            case: "success",
            value: {
              target: { case: "session", value: { settings: current, session: { sessionId: sessionID } } },
            },
          },
        }),
      },
    ]);
    await expect(new ApiClient(transport).chat.getSettings(sessionTarget)).resolves.toMatchObject({
      kind: "session",
      settings: {
        thinking: { kind: "unsupported" },
        fast: { kind: "unsupported" },
        agentEditability: { kind: "workflow_lock" },
        questions: { capable: false, enabled: true, editability: { kind: "editable" } },
        autoCompaction: {
          policy: "required",
          stored: false,
          effective: true,
          editability: { kind: "workflow_lock" },
        },
      },
      session: { sessionID, task: null, previousSessionID: null },
    });
  });

  it("decodes custom Thinking, caching lock and disabled Auto-compaction with applied no-op", async () => {
    const response = mutationResponse();
    if (response.outcome.case !== "success" || response.outcome.value.settings === undefined)
      throw new Error("Incomplete fixture.");
    const current = response.outcome.value.settings;
    if (current.thinking === undefined || current.autoCompaction === undefined)
      throw new Error("Incomplete fixture.");
    current.thinking = {
      ...current.thinking,
      kind: ThinkingKind.CUSTOM,
      value: "precise custom effort",
      baselineValue: "custom baseline",
      values: [],
    };
    current.cachingLocked = true;
    current.agentLocked = true;
    current.agentEditability = Editability.CACHING_LOCK;
    current.autoCompaction = {
      ...current.autoCompaction,
      policy: AutoCompactionPolicy.DISABLED,
      stored: true,
      effective: false,
      editability: Editability.POLICY_DISABLED,
    };
    response.outcome.value.result = create(MutationResultSchema, {
      outcome: { case: "applied", value: { changed: false } },
    });
    const transport = new FakeRpcTransport([
      { descriptor: ChatSettingsService.method.mutate, result: response },
    ]);
    await expect(
      new ApiClient(transport).chat.mutateSettings(sessionTarget, {
        kind: "thinking",
        value: "precise custom effort",
      }),
    ).resolves.toMatchObject({
      result: { kind: "applied", changed: false },
      settings: {
        thinking: { kind: "custom", value: "precise custom effort", baselineValue: "custom baseline" },
        agentEditability: { kind: "caching_lock" },
        autoCompaction: {
          policy: "disabled",
          stored: true,
          effective: false,
          editability: { kind: "policy_disabled" },
        },
      },
    });
  });

  it("rejects Session identity mismatch on both reads and mutations", async () => {
    const otherSessionID = "323e4567-e89b-42d3-a456-426614174000";
    const mutation = mutationResponse();
    if (mutation.outcome.case !== "success" || mutation.outcome.value.session === undefined)
      throw new Error("Incomplete fixture.");
    mutation.outcome.value.session.sessionId = otherSessionID;
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatSettingsService.method.read,
        result: create(ReadResultSchema, {
          outcome: {
            case: "success",
            value: {
              target: {
                case: "session",
                value: {
                  settings: settings(),
                  session: { sessionId: otherSessionID },
                },
              },
            },
          },
        }),
      },
      { descriptor: ChatSettingsService.method.mutate, result: mutation },
    ]);
    const chat = new ApiClient(transport).chat;
    await expect(chat.getSettings(sessionTarget)).rejects.toBeInstanceOf(ContractError);
    await expect(chat.mutateSettings(sessionTarget, { kind: "fast", enabled: true })).rejects.toBeInstanceOf(
      ContractError,
    );
  });

  it("rejects a read response for the other target kind", async () => {
    const first = choice();
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatSettingsService.method.read,
        resultFactory: (_request, index) =>
          create(ReadResultSchema, {
            outcome: {
              case: "success",
              value: {
                target:
                  index === 0
                    ? { case: "session", value: { settings: settings(), session: { sessionId: sessionID } } }
                    : { case: "newChat", value: { choices: [first], initialSettings: first.baseline } },
              },
            },
          }),
      },
    ]);
    const chat = new ApiClient(transport).chat;
    await expect(chat.getSettings(newChatTarget)).rejects.toBeInstanceOf(ContractError);
    await expect(chat.getSettings(sessionTarget)).rejects.toBeInstanceOf(ContractError);
  });

  it("rejects unpaired Task facts without manufacturing a navigation target", async () => {
    for (const taskFacts of [{ taskId: "task-existing" }, { taskShortId: "KENT-416" }]) {
      const transport = new FakeRpcTransport([
        {
          descriptor: ChatSettingsService.method.read,
          result: create(ReadResultSchema, {
            outcome: {
              case: "success",
              value: {
                target: {
                  case: "session",
                  value: { settings: settings(), session: { sessionId: sessionID, ...taskFacts } },
                },
              },
            },
          }),
        },
      ]);
      await expect(new ApiClient(transport).chat.getSettings(sessionTarget)).rejects.toBeInstanceOf(Error);
    }
  });

  it("rejects impossible Settings capability, editability, and policy descriptors", async () => {
    const valid = settings();
    if (
      valid.thinking === undefined ||
      valid.autoCompaction === undefined ||
      valid.selectedAgent === undefined
    )
      throw new Error("Incomplete fixture.");
    const malformed = [
      create(SettingsSchema, { ...valid, thinking: { ...valid.thinking, values: ["low"] } }),
      create(SettingsSchema, { ...valid, thinking: { ...valid.thinking, kind: ThinkingKind.CUSTOM } }),
      create(SettingsSchema, { ...valid, agentEditability: Editability.UNSPECIFIED }),
      create(SettingsSchema, { ...valid, agentLocked: true }),
      create(SettingsSchema, {
        ...valid,
        autoCompaction: { ...valid.autoCompaction, policy: AutoCompactionPolicy.REQUIRED },
      }),
      create(SettingsSchema, { ...valid, autoCompaction: { ...valid.autoCompaction, effective: false } }),
      create(SettingsSchema, { ...valid, selectedAgent: { ...valid.selectedAgent, role: " " } }),
      create(SettingsSchema, { ...valid, supervisor: undefined }),
    ];
    for (const invalid of malformed) {
      const transport = new FakeRpcTransport([
        {
          descriptor: ChatSettingsService.method.read,
          result: create(ReadResultSchema, {
            outcome: {
              case: "success",
              value: {
                target: { case: "session", value: { settings: invalid, session: { sessionId: sessionID } } },
              },
            },
          }),
        },
      ]);
      await expect(new ApiClient(transport).chat.getSettings(sessionTarget)).rejects.toBeInstanceOf(Error);
    }
  });

  it("rejects mutation responses missing an outcome, authoritative projections, or valid Context", async () => {
    const response = mutationResponse();
    if (response.outcome.case !== "success" || response.outcome.value.context === undefined)
      throw new Error("Incomplete fixture.");
    const valid = response.outcome.value;
    const context = response.outcome.value.context;
    const malformed = [
      create(MutationResponseSchema),
      create(MutationResponseSchema, { outcome: { case: "success", value: {} } }),
      create(MutationResponseSchema, {
        outcome: { case: "success", value: { ...valid, result: create(MutationResultSchema) } },
      }),
      create(MutationResponseSchema, {
        outcome: {
          case: "success",
          value: {
            ...valid,
            result: create(MutationResultSchema, {
              outcome: { case: "rejected", value: { reason: MutationRejectionReason.UNSPECIFIED } },
            }),
          },
        },
      }),
      create(MutationResponseSchema, {
        outcome: { case: "success", value: { ...valid, session: undefined } },
      }),
      create(MutationResponseSchema, {
        outcome: { case: "success", value: { ...valid, settings: undefined } },
      }),
      create(MutationResponseSchema, {
        outcome: { case: "success", value: { ...valid, context: undefined } },
      }),
      create(MutationResponseSchema, {
        outcome: {
          case: "success",
          value: {
            ...valid,
            context: { ...context, remainingTokens: 81n },
          },
        },
      }),
      create(MutationResponseSchema, {
        outcome: {
          case: "success",
          value: {
            ...valid,
            context: { ...context, contextWindowTokens: 9007199254740992n },
          },
        },
      }),
    ];
    for (const invalid of malformed) {
      const transport = new FakeRpcTransport([
        { descriptor: ChatSettingsService.method.mutate, result: invalid },
      ]);
      await expect(
        new ApiClient(transport).chat.mutateSettings(sessionTarget, { kind: "fast", enabled: true }),
      ).rejects.toBeInstanceOf(Error);
    }
  });

  it("rejects incomplete or contradictory New Chat baseline descriptors", async () => {
    const first = choice();
    const malformed = [
      { choices: [first] },
      { choices: [], initialSettings: first.baseline },
      { choices: [{ ...first, baseline: undefined }], initialSettings: first.baseline },
      {
        choices: [{ ...first, baseline: { ...first.baseline, fast: undefined } }],
        initialSettings: first.baseline,
      },
      {
        choices: [{ ...first, baseline: { ...first.baseline, agentRole: "other" } }],
        initialSettings: first.baseline,
      },
      {
        choices: [{ ...first, baseline: { ...first.baseline, supervisor: SupervisorValue.OFF } }],
        initialSettings: first.baseline,
      },
      { choices: [first], initialSettings: { ...first.baseline, questionsEnabled: undefined } },
    ];
    for (const invalid of malformed) {
      const transport = new FakeRpcTransport([
        {
          descriptor: ChatSettingsService.method.read,
          result: create(ReadResultSchema, {
            outcome: {
              case: "success",
              value: {
                target: { case: "newChat", value: invalid },
              },
            },
          }),
        },
      ]);
      await expect(new ApiClient(transport).chat.getSettings(newChatTarget)).rejects.toBeInstanceOf(Error);
    }
  });

  it("preserves the shared Chat error variants instead of flattening them into Settings errors", async () => {
    const cases: readonly Readonly<{
      wire: ReturnType<typeof create<typeof ReadErrorSchema>>;
      expected: ChatError;
    }>[] = [
      {
        wire: create(ReadErrorSchema, {
          code: "session_not_found",
          detail: { case: "sessionNotFound", value: { sessionId: sessionID } },
        }),
        expected: { kind: "session_not_found", sessionID },
      },
      {
        wire: create(ReadErrorSchema, {
          code: "workspace_not_registered",
          detail: { case: "workspaceNotRegistered", value: {} },
        }),
        expected: { kind: "workspace_not_registered" },
      },
      {
        wire: create(ReadErrorSchema, { code: "auth_required", detail: { case: "authRequired", value: {} } }),
        expected: { kind: "auth_required" },
      },
      {
        wire: create(ReadErrorSchema, {
          code: "server_not_ready",
          detail: { case: "serverNotReady", value: { reason: ServerNotReadyReason.ONBOARDING_REQUIRED } },
        }),
        expected: { kind: "server_not_ready" },
      },
      {
        wire: create(ReadErrorSchema, {
          code: "internal_failure",
          detail: { case: "internalFailure", value: { operation: "settings.commit", cause: "disk full" } },
        }),
        expected: { kind: "internal_failure", operation: "settings.commit", cause: "disk full" },
      },
      {
        wire: create(ReadErrorSchema, { code: "future_error" }),
        expected: { kind: "unknown", code: "future_error" },
      },
    ];
    for (const { wire, expected } of cases) {
      const transport = new FakeRpcTransport([
        {
          descriptor: ChatSettingsService.method.read,
          result: create(ReadResultSchema, { outcome: { case: "error", value: wire } }),
        },
        {
          descriptor: ChatSettingsService.method.mutate,
          result: create(MutationResponseSchema, { outcome: { case: "error", value: wire } }),
        },
      ]);
      const chat = new ApiClient(transport).chat;
      for (const operation of [
        async () => chat.getSettings(sessionTarget),
        async () => chat.mutateSettings(sessionTarget, { kind: "questions", enabled: false }),
      ]) {
        const failure: unknown = await operation().catch((cause: unknown) => cause);
        expect(failure).toBeInstanceOf(ChatOperationError);
        expect(failure).toMatchObject({ detail: expected });
      }
    }
  });

  it.each([
    { mode: CompactionMode.DISABLED, expected: "disabled" },
    { mode: CompactionMode.PROVIDER_NATIVE, expected: "provider_native" },
  ])("preserves $expected Chat Context mode in a mutation result", async ({ mode, expected }) => {
    const response = mutationResponse();
    if (response.outcome.case !== "success" || response.outcome.value.context === undefined)
      throw new Error("Incomplete fixture.");
    response.outcome.value.context.compactionMode = mode;
    const transport = new FakeRpcTransport([
      { descriptor: ChatSettingsService.method.mutate, result: response },
    ]);
    await expect(
      new ApiClient(transport).chat.mutateSettings(sessionTarget, { kind: "questions", enabled: false }),
    ).resolves.toMatchObject({
      context: { compactionMode: expected },
    });
  });

  it("passes only the selected values to creation and preserves unsupported capability absence", async () => {
    const first = choice();
    const initial = { ...first.baseline, thinking: undefined, fast: undefined };
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatSettingsService.method.read,
        result: create(ReadResultSchema, {
          outcome: {
            case: "success",
            value: {
              target: {
                case: "newChat",
                value: {
                  choices: [{ ...first, baseline: initial, thinking: undefined, fast: undefined }],
                  initialSettings: initial,
                },
              },
            },
          },
        }),
      },
      {
        descriptor: ChatService.method.queue,
        result: create(ChatService.method.queue.output, {
          outcome: {
            case: "success",
            value: {
              session: { sessionId: sessionID },
              outcome: {
                case: "notAccepted",
                value: { reason: { case: "runtimeUnavailable", value: {} } },
              },
            },
          },
        }),
      },
    ]);
    const chat = new ApiClient(transport).chat;
    const result = await chat.getSettings(newChatTarget);
    if (result.kind !== "new_chat") throw new Error("Expected New Chat.");
    expect(result.initialSettings).toEqual({
      agentRole: "default",
      supervisor: "edits",
      thinking: null,
      fast: null,
      questionsEnabled: true,
      autoCompactionEnabled: true,
    });
    expect(result.catalog.choices[0]).toMatchObject({
      baseline: result.initialSettings,
      thinking: { kind: "unsupported" },
      fast: { kind: "unsupported" },
    });
    await chat.queue(
      { ...newChatTarget, initialSettings: result.initialSettings },
      { kind: "text", text: "start" },
    );
    expect(transport.attachedProjectDescriptorCalls[1]?.request).toEqual(
      create(ChatService.method.queue.input, {
        target: {
          target: {
            case: "newChat",
            value: {
              projectId: "project-1",
              workspaceId: "workspace-1",
              initialSettings: initial,
            },
          },
        },
        activation: { input: { case: "text", value: "start" } },
      }),
    );
  });
});
