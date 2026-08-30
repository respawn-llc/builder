import { ApiClient } from "./client";
import { ContractError } from "./errors";
import { createJsonRpcTransport } from "./jsonRpc";
import { FakeRpcTransport } from "@/test-support/api";
import { z } from "zod";
import { create, decodeEnvelope, encode, encodeEnvelope, operationName } from "@app/server-api-contract";
import {
  BackgroundShellOutputMode,
  CacheWarningMode,
  CompactionMode,
  ModelVerbosity,
  SessionLaunchService,
  SessionPlanResultSchema,
  SettingsSchema,
  SourceReportSchema,
  ShellPostprocessingMode,
  SleepPreventionMode,
  ToolID,
  WorkflowCompletionMode,
} from "@app/server-api-contract/gen/kent/api/session_launch/session_launch_pb";
import {
  AttachSessionResultSchema,
  ConnectionService,
  HandshakeResultSchema,
} from "@app/server-api-contract/gen/kent/api/connection/connection_pb";

const sessionID = "123e4567-e89b-42d3-a456-426614174000";
const target = {
  projectID: "project-1",
  workspace: { workspaceID: "workspace-1" },
  sessionID,
} as const;

class ChatMockWebSocket extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;

  readonly sent: (string | Uint8Array)[] = [];
  binaryType: BinaryType = "arraybuffer";
  readyState = ChatMockWebSocket.CONNECTING;

  constructor(readonly url: string) {
    super();
    chatSockets.push(this);
  }

  send(data: string | ArrayBufferLike | Blob | ArrayBufferView): void {
    const text = z.string().safeParse(data);
    if (text.success) {
      this.sent.push(text.data);
      return;
    }
    if (ArrayBuffer.isView(data)) {
      this.sent.push(new Uint8Array(data.buffer, data.byteOffset, data.byteLength).slice());
      return;
    }
    if (data instanceof ArrayBuffer) {
      this.sent.push(new Uint8Array(data).slice());
      return;
    }
    throw new Error("Chat mock WebSocket does not support Blob sends.");
  }

  close(): void {
    if (this.readyState === ChatMockWebSocket.CLOSED) return;
    this.readyState = ChatMockWebSocket.CLOSED;
    this.dispatchEvent(new Event("close"));
  }

  open(): void {
    this.readyState = ChatMockWebSocket.OPEN;
    this.dispatchEvent(new Event("open"));
  }

  receive(data: string | ArrayBuffer): void {
    this.dispatchEvent(new MessageEvent("message", { data }));
  }
}

const chatSockets: ChatMockWebSocket[] = [];

function runtimePlanResult(planSessionID: string) {
  const settings = create(SettingsSchema, {
    model: "gpt-5",
    thinkingLevel: "medium",
    modelVerbosity: ModelVerbosity.MEDIUM,
    systemPromptFiles: [],
    modelCapabilities: { supportsReasoningEffort: true, supportsVisionInputs: false },
    theme: "auto",
    notificationMethod: "off",
    toolPreambles: true,
    priorityRequestMode: false,
    debug: false,
    serverHost: "127.0.0.1",
    serverPort: 53082,
    webSearch: "off",
    providerOverride: "openai",
    providerIdentifier: "openai",
    openaiBaseUrl: "",
    providerCapabilities: {
      providerId: "openai",
      supportsResponsesApi: true,
      supportsResponsesCompact: true,
      supportsRequestInputTokenCount: false,
      supportsPromptCacheKey: true,
      supportsNativeWebSearch: false,
      supportsReasoningEncrypted: false,
      supportsServerSideContextEdit: false,
      supportsProviderVerbosity: true,
      isOpenaiFirstParty: true,
    },
    store: true,
    allowNonCwdEdits: false,
    modelContextWindow: 100000,
    contextCompactionThresholdTokens: 80000,
    preSubmitCompactionLeadTokens: 1000,
    minimumExecToBgSeconds: 1,
    compactionMode: CompactionMode.LOCAL,
    enabledTools: [{ toolId: ToolID.TOOL_ID_EXEC_COMMAND, enabled: true }],
    skillToggles: [{ key: "example", value: true }],
    timeouts: { modelRequestSeconds: 30 },
    shellOutputMaxChars: 10000,
    bgShellsOutput: BackgroundShellOutputMode.DEFAULT,
    shell: { postprocessingMode: ShellPostprocessingMode.BUILTIN },
    cacheWarningMode: CacheWarningMode.DEFAULT,
    worktrees: { baseDir: "", setupScript: "", setupTimeoutSeconds: 0 },
    workflow: {
      completionMode: WorkflowCompletionMode.AUTO,
      concurrency: 1,
      maxInvalidCompletionAttempts: 1,
      useRequiredToolCalls: false,
      subagents: false,
    },
    reviewer: {
      frequency: "off",
      model: "gpt-5",
      thinkingLevel: "medium",
      modelVerbosity: ModelVerbosity.MEDIUM,
      providerOverride: "openai",
      openaiBaseUrl: "",
      modelCapabilities: { supportsReasoningEffort: true, supportsVisionInputs: false },
      providerCapabilities: {
        providerId: "openai",
        supportsResponsesApi: true,
        supportsResponsesCompact: true,
        supportsRequestInputTokenCount: false,
        supportsPromptCacheKey: true,
        supportsNativeWebSearch: false,
        supportsReasoningEncrypted: false,
        supportsServerSideContextEdit: false,
        supportsProviderVerbosity: true,
        isOpenaiFirstParty: true,
      },
      modelContextWindow: 100000,
      auth: "",
      systemPromptFile: "",
      timeoutSeconds: 30,
      verboseOutput: false,
    },
    subagents: [],
    maxSubagentDepth: 0,
    preventSleep: SleepPreventionMode.ACTIVE,
  });
  return create(SessionPlanResultSchema, {
    outcome: {
      case: "success",
      value: {
        plan: {
          sessionId: planSessionID,
          activeSettings: settings,
          enabledToolIds: [ToolID.TOOL_ID_EXEC_COMMAND],
          modelContractLocked: false,
          questionsEnabled: true,
          autoCompactionEnabled: true,
          thinkingOverrideExplicit: false,
          source: create(SourceReportSchema, { sources: [] }),
        },
        warnings: [],
      },
    },
  });
}

function transcriptHydrationPayload() {
  return {
    SessionIdentity: {
      SessionID: sessionID,
      SessionName: null,
      ConversationFreshness: 0,
      ExecutionTarget: null,
    },
    SessionStatus: {
      ReviewerFrequency: "off",
      ReviewerEnabled: false,
      AutoCompactionEnabled: true,
      QuestionsEnabled: true,
      FastModeAvailable: false,
      FastModeEnabled: false,
      ThinkingLevel: "medium",
      CompactionMode: "local",
      CompactionCount: 0,
      PreviousSessionID: null,
      ParentAgentSessionID: null,
      NavigationTargetSessionID: null,
      Workflow: null,
    },
    RuntimeReadModelUpdate: {
      Version: { Epoch: "epoch-1", Generation: 1, Sequence: 1 },
      Activity: {
        State: "registered_idle",
        ActiveStep: null,
        Reviewer: "inactive",
        QueueAccepting: true,
        DiagnosticRecovery: false,
      },
    },
    CommittedRows: [],
    ActiveAssistant: null,
    ActiveThinkingStatus: null,
    ActiveReasoningTraces: [],
    ActiveStep: null,
    ActiveCompaction: null,
    InFlightTools: [],
    PendingPrompts: [],
    BackgroundActivities: [],
    ContextUsage: null,
    GoalStatus: null,
  };
}

describe("Desktop Chat read client", () => {
  it("reads Main View, Context, and both Chat Settings targets", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "session.getMainView",
        result: {
          MainView: {
            Version: { Epoch: "epoch-1", Generation: 1, Sequence: 1 },
            Status: {
              ReviewerFrequency: "off",
              ReviewerEnabled: false,
              AutoCompactionEnabled: true,
              QuestionsEnabled: true,
              FastModeAvailable: false,
              FastModeEnabled: false,
              ConversationFreshness: 0,
              PreviousSessionID: null,
              ParentAgentSessionID: null,
              NavigationTargetSessionID: null,
              LastCommittedAssistantFinalAnswer: null,
              ThinkingLevel: "medium",
              CompactionMode: "local",
              ContextUsage: {
                UsedTokens: 4,
                WindowTokens: 100,
                CacheHitPercent: 0,
                HasCacheHitPercentage: false,
              },
              CompactionCount: 0,
              Goal: null,
              WorkflowSession: null,
            },
            Session: {
              SessionID: sessionID,
              SessionName: "",
              AgentRole: null,
              ConversationFreshness: 0,
              ExecutionTarget: {
                WorkspaceID: "",
                WorkspaceName: "Workspace",
                WorkspaceRoot: "/workspace",
                WorkspaceAvailability: "unlinked",
                Worktree: null,
                CwdRelpath: ".",
                EffectiveWorkdir: "/workspace",
              },
            },
            Activity: {
              State: "registered_idle",
              ActiveStep: null,
              Reviewer: "inactive",
              QueueAccepting: true,
              DiagnosticRecovery: false,
            },
          },
        },
      },
      {
        method: "chat.context.get",
        result: {
          context: {
            context_window_tokens: 100,
            used_tokens: 4,
            remaining_tokens: 96,
            automatic_threshold_tokens: 80,
            auto_compaction_enabled: true,
            compaction_mode: "local",
            completed_compaction_count: 0,
            compaction_running: false,
            manual_compact_available: true,
          },
        },
      },
      {
        method: "chat.settings.read",
        handler: (params) => ({
          settings: {
            selected_agent: { role: "default", model: "gpt-5", thinking: "medium" },
            agent_choices: [],
            agent_editability: "editable",
            supervisor: { value: "off", baseline: "off", editability: "editable" },
            thinking: {
              kind: "enumerated",
              value: "medium",
              baseline_value: "medium",
              values: ["low", "medium"],
              editability: "editable",
            },
            fast: { value: false, editability: "editable" },
            questions: { capable: true, enabled: true, editability: "editable" },
            auto_compaction: {
              policy: "optional",
              stored: true,
              effective: true,
              editability: "editable",
            },
            agent_locked: false,
            workflow_locked: false,
            caching_locked: false,
          },
          ...(z.object({ target: z.object({ kind: z.string() }) }).parse(params).target.kind === "session"
            ? { session: { session_id: sessionID, previous_session_id: null, task_id: null } }
            : {}),
        }),
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.chat.getMainView(target)).resolves.toMatchObject({
      sessionID,
      sessionName: null,
      executionTarget: { workspaceID: "", workspaceAvailability: "unlinked" },
    });
    await expect(client.chat.getContext(target)).resolves.toMatchObject({
      contextWindowTokens: 100,
      remainingTokens: 96,
    });
    await expect(client.chat.getSettings({ ...target, kind: "lazy" })).resolves.toMatchObject({
      selectedAgent: { role: "default" },
      session: null,
    });
    await expect(client.chat.getSettings({ ...target, kind: "session" })).resolves.toMatchObject({
      session: { sessionID },
    });

    const mismatched = new ApiClient(
      new FakeRpcTransport([
        {
          method: "chat.settings.read",
          result: {
            settings: {
              selected_agent: { role: "default", model: "gpt-5", thinking: "medium" },
              agent_choices: [],
              agent_editability: "editable",
              supervisor: { value: "off", baseline: "off", editability: "editable" },
              questions: { capable: true, enabled: true, editability: "editable" },
              auto_compaction: {
                policy: "optional",
                stored: true,
                effective: true,
                editability: "editable",
              },
              agent_locked: false,
              workflow_locked: false,
              caching_locked: false,
            },
            session: { session_id: "223e4567-e89b-42d3-a456-426614174000" },
          },
        },
      ]),
    );
    await expect(mismatched.chat.getSettings({ ...target, kind: "session" })).rejects.toBeInstanceOf(
      ContractError,
    );

    const activationTransport = new FakeRpcTransport([
      { descriptor: SessionLaunchService.method.plan, result: runtimePlanResult(sessionID) },
      {
        method: "session.runtime.activate",
        result: { attachment: { session_id: sessionID, generation: 7 } },
      },
      { method: "session.runtime.release", result: { released: true, active: false } },
    ]);
    const activationClient = new ApiClient(activationTransport);
    await expect(activationClient.chat.activateRuntime(target)).resolves.toEqual({
      sessionID,
      generation: 7,
    });
    await expect(activationClient.chat.releaseRuntime({ sessionID, generation: 7 })).resolves.toEqual({
      released: true,
      active: false,
    });

    const mismatchedPlanClient = new ApiClient(
      new FakeRpcTransport([
        {
          descriptor: SessionLaunchService.method.plan,
          result: runtimePlanResult("223e4567-e89b-42d3-a456-426614174000"),
        },
      ]),
    );
    await expect(mismatchedPlanClient.chat.activateRuntime(target)).rejects.toBeInstanceOf(ContractError);
  });

  it("reads bounded transcript pages in both cursor directions", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([
        {
          method: "session.getTranscriptPage",
          handler: (_params, callIndex) => ({
            transcript: {
              SessionID: sessionID,
              SessionName: "",
              ConversationFreshness: callIndex,
              OlderCursor: 11,
              HasMoreAbove: true,
              NewerCursor: 29,
              HasMoreBelow: false,
              LatestRollbackCandidate: { user_message_seq: 7, candidate_page_end_byte: 100 },
              Entries: [
                {
                  Visibility: "ongoing",
                  Integrity: 0,
                  Kind: "assistant",
                  Locator: { event_sequence: 7, row_ordinal: 1 },
                  User: null,
                  Assistant: {
                    StepID: sessionID,
                    StreamID: null,
                    Text: "done",
                    CondensedText: null,
                    Phase: "final_answer",
                    committed_at_unix_ms: null,
                  },
                  Tool: null,
                  ReasoningTrace: null,
                  Notice: null,
                  ReviewerFeedback: null,
                  ReviewerError: null,
                },
              ],
            },
          }),
        },
      ]),
    );

    await expect(
      client.chat.getTranscriptPage(target, { direction: "older", value: 41 }),
    ).resolves.toMatchObject({
      sessionID,
      sessionName: null,
      olderCursor: 11,
      newerCursor: 29,
      entries: [{ Kind: "assistant", Locator: { event_sequence: 7, row_ordinal: 1 } }],
    });
    await expect(
      client.chat.getTranscriptPage(target, { direction: "newer", value: 29 }),
    ).resolves.toMatchObject({
      sessionID,
      conversationFreshness: 1,
      hasMoreBelow: false,
    });
  });

  it("delivers transcript hydration and live events with recoverable contract errors and typed completion", async () => {
    const events: unknown[] = [];
    const errors: Error[] = [];
    const completions: unknown[] = [];
    const transport = new FakeRpcTransport([]);
    const client = new ApiClient(transport);
    client.chat.subscribeTranscript(target, {
      onEvent: (event) => events.push(event),
      onComplete: (completion) => completions.push(completion),
      onError: (error) => errors.push(error),
    });

    transport.emit("session.transcript", {
      message: { Sequence: 1, Kind: "hydration", Payload: transcriptHydrationPayload() },
    });
    transport.emit("session.transcript", {
      message: {
        Sequence: 2,
        Kind: "session_identity",
        Payload: {
          SessionID: "223e4567-e89b-42d3-a456-426614174000",
          SessionName: null,
          ConversationFreshness: 0,
          ExecutionTarget: null,
        },
      },
    });
    transport.emit("session.transcript", {
      message: {
        Sequence: 3,
        Kind: "session_identity",
        Payload: { SessionID: sessionID, SessionName: null, ConversationFreshness: 0, ExecutionTarget: null },
      },
    });
    transport.complete("session.subscribeTranscript", 17, "subscriber overflow", "subscriber_overflow");

    expect(events).toHaveLength(2);
    expect(events[0]).toMatchObject({ sequence: 1, kind: "hydration" });
    expect(events[1]).toMatchObject({ sequence: 3, kind: "session_identity" });
    expect(errors).toHaveLength(1);
    expect(errors[0]).toBeInstanceOf(ContractError);
    expect(completions).toEqual([
      { code: 17, message: "subscriber overflow", reason: "subscriber_overflow" },
    ]);

    vi.stubGlobal("WebSocket", ChatMockWebSocket);
    const socketEvents: unknown[] = [];
    const socketErrors: Error[] = [];
    const socketCompletions: unknown[] = [];
    const socketClient = new ApiClient(createJsonRpcTransport("ws://127.0.0.1:53082/rpc"));
    const socketSubscription = socketClient.chat.subscribeTranscript(target, {
      onEvent: (event) => socketEvents.push(event),
      onComplete: (completion) => socketCompletions.push(completion),
      onError: (error) => socketErrors.push(error),
    });

    const firstSocket = chatSockets[0] ?? failTest("first Chat socket missing");
    await setupChatSocket(firstSocket);
    firstSocket.receive(
      JSON.stringify({
        jsonrpc: "2.0",
        method: "session.transcript",
        params: { message: { Sequence: 1, Kind: "hydration", Payload: transcriptHydrationPayload() } },
      }),
    );
    firstSocket.receive(
      JSON.stringify({
        jsonrpc: "2.0",
        method: "session.transcript.complete",
        params: { code: 17, message: "subscriber overflow", transcript_close_reason: "subscriber_overflow" },
      }),
    );

    await vi.waitFor(() => {
      expect(chatSockets).toHaveLength(2);
    });
    const secondSocket = chatSockets[1] ?? failTest("reconnected Chat socket missing");
    await setupChatSocket(secondSocket);
    secondSocket.receive(
      JSON.stringify({
        jsonrpc: "2.0",
        method: "session.transcript.complete",
        params: { code: "malformed" },
      }),
    );
    await vi.waitFor(() => {
      expect(socketErrors).toHaveLength(2);
    });
    expect(socketEvents).toHaveLength(1);
    expect(socketCompletions).toEqual([
      { code: 17, message: "subscriber overflow", reason: "subscriber_overflow" },
    ]);
    expect(chatSockets).toHaveLength(2);
    socketSubscription.close();

    const closeErrors: Error[] = [];
    const closeSubscription = socketClient.chat.subscribeTranscript(target, {
      onEvent: () => undefined,
      onComplete: () => undefined,
      onError: (error) => closeErrors.push(error),
    });
    const thirdSocket = chatSockets[2] ?? failTest("close-test Chat socket missing");
    await setupChatSocket(thirdSocket);
    thirdSocket.close();
    await vi.waitFor(() => {
      expect(chatSockets).toHaveLength(4);
    });
    expect(closeErrors).toHaveLength(1);
    const fourthSocket = chatSockets[3] ?? failTest("close-test reconnect socket missing");
    await setupChatSocket(fourthSocket);
    closeSubscription.close();
    vi.unstubAllGlobals();
  });
});

async function setupChatSocket(socket: ChatMockWebSocket): Promise<void> {
  socket.open();
  await waitForChatSent(socket, 1);
  binaryAckChat(socket, 0, ConnectionService.method.handshake, handshakeResult());
  await waitForChatSent(socket, 2);
  binaryAckChat(
    socket,
    1,
    ConnectionService.method.attachSession,
    create(AttachSessionResultSchema, {
      outcome: {
        case: "success",
        value: {
          attachment: {
            case: "session",
            value: {
              projectId: target.projectID,
              workspaceId: target.workspace.workspaceID,
              workspaceRoot: "/workspace",
              sessionId: target.sessionID,
            },
          },
        },
      },
    }),
  );
  await waitForChatSent(socket, 3);
  const subscribe = chatJsonFrame(socket, 2);
  socket.receive(JSON.stringify({ jsonrpc: "2.0", id: subscribe.id, result: {} }));
  await flushPromises();
}

function handshakeResult() {
  return create(HandshakeResultSchema, {
    outcome: {
      case: "success",
      value: {
        identity: {
          protocolVersion: "126",
          serverId: "server-1",
          pid: 1,
        },
      },
    },
  });
}

function binaryAckChat<
  Method extends typeof ConnectionService.method.handshake | typeof ConnectionService.method.attachSession,
>(
  socket: ChatMockWebSocket,
  sentIndex: number,
  method: Method,
  result: ReturnType<typeof create<Method["output"]>>,
): void {
  const raw = socket.sent[sentIndex] ?? failTest(`Chat socket frame ${sentIndex.toString()} missing`);
  if (!(raw instanceof Uint8Array)) throw new Error("Chat socket setup frame is not binary.");
  const call = decodeEnvelope(raw).frame;
  if (
    call.case !== "call" ||
    call.value.operation !== operationName(method) ||
    call.value.correlation === undefined
  ) {
    throw new Error("Chat socket setup frame has the wrong descriptor operation.");
  }
  const payload = encode(method.output, result);
  const response = encodeEnvelope({
    frame: {
      case: "result",
      value: {
        operation: operationName(method),
        correlation: call.value.correlation,
        payload,
      },
    },
  });
  const responseBuffer = new ArrayBuffer(response.byteLength);
  new Uint8Array(responseBuffer).set(response);
  socket.receive(responseBuffer);
}

function chatJsonFrame(
  socket: ChatMockWebSocket,
  sentIndex: number,
): Readonly<{ id: string; method: string }> {
  const raw = socket.sent[sentIndex] ?? failTest(`Chat socket text frame ${sentIndex.toString()} missing`);
  if (raw instanceof Uint8Array) throw new Error("Chat socket frame is binary.");
  const parsed: unknown = JSON.parse(raw);
  const result = z.object({ id: z.string(), method: z.string() }).safeParse(parsed);
  if (!result.success) throw new Error("Chat socket frame is not a JSON-RPC request.");
  return result.data;
}

async function waitForChatSent(socket: ChatMockWebSocket, count: number): Promise<void> {
  await vi.waitFor(() => {
    expect(socket.sent.length).toBeGreaterThanOrEqual(count);
  });
}

async function flushPromises(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

function failTest(message: string): never {
  throw new Error(message);
}
