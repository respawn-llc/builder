import { create } from "@app/server-api-contract";
import { ChatService } from "@app/server-api-contract/gen/kent/api/chat/chat_pb";
import { AgentPreparationCategory } from "@app/server-api-contract/gen/kent/api/chat_settings/chat_settings_pb";
import {
  LiveService,
  LiveStopStatus,
  PendingWorkItemKind,
  PendingWorkItemState,
  PendingWorkLane,
  TurnService,
} from "@app/server-api-contract/gen/kent/api/runtime/runtime_pb";
import {
  SessionAuthPreparation,
  SessionLifecycleService,
  SessionTransitionAction,
} from "@app/server-api-contract/gen/kent/api/session_launch/session_lifecycle_pb";

import { FakeRpcTransport } from "@/test-support/api";

import { ApiClient } from "./client";
import { ChatOperationError, chatErrorMessage } from "./chatErrors";
import { ContractError } from "./errors";
import { parsePendingWorkItemID } from "./pendingWork";

const sessionID = "123e4567-e89b-42d3-a456-426614174000";
const queueItemID = "223e4567-e89b-42d3-a456-426614174000";
const sessionTarget = {
  kind: "session",
  projectID: "project-1",
  workspace: { workspaceID: "workspace-1" },
  sessionID,
} as const;
const newChatTarget = {
  kind: "new_chat",
  projectID: "project-1",
  workspace: { workspaceRoot: "/workspace" },
  initialSettings: {
    agentRole: "default",
    supervisor: "edits",
    thinking: "high",
    fast: true,
    questionsEnabled: false,
    autoCompactionEnabled: true,
  },
} as const;

describe("Desktop Chat mutation client", () => {
  it("Steers an existing Session and returns the accepted domain result", async () => {
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatService.method.steer,
        result: create(ChatService.method.steer.output, {
          outcome: {
            case: "success",
            value: {
              session: { sessionId: sessionID },
              outcome: {
                case: "accepted",
                value: { queueItem: { id: queueItemID } },
              },
            },
          },
        }),
      },
    ]);
    const client = new ApiClient(transport);

    const result = await client.chat.steer(sessionTarget, { kind: "text", text: "continue" });
    expect(result.sessionID).toBe(sessionID);
    expect(result.outcome.kind).toBe("accepted");
    if (result.outcome.kind !== "accepted") throw new Error("Expected accepted Steer fixture.");
    expect(result.outcome.queueItemID.toJSONValue()).toBe(queueItemID);
    expect(result.outcome.diagnostic).toBeNull();
    expect(transport.descriptorCalls).toHaveLength(1);
    expect(transport.descriptorCalls[0]?.request).toMatchObject({
      target: { target: { case: "session", value: { sessionId: sessionID } } },
      activation: { input: { case: "text", value: "continue" } },
    });
  });

  it("Queues a command for a New Chat with complete initial settings", async () => {
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatService.method.queue,
        result: create(ChatService.method.queue.output, {
          outcome: {
            case: "success",
            value: {
              session: { sessionId: sessionID },
              outcome: {
                case: "accepted",
                value: { queueItem: { id: queueItemID } },
              },
            },
          },
        }),
      },
    ]);
    const client = new ApiClient(transport);
    const command = {
      kind: "command",
      catalogIdentity: "builtin:review",
      token: "/review",
      separatorWhitespace: "\t",
      arguments: "working tree",
    } as const;

    await expect(client.chat.queue(newChatTarget, command)).resolves.toMatchObject({
      sessionID,
      outcome: { kind: "accepted" },
    });
    expect(transport.attachedProjectDescriptorCalls).toHaveLength(1);
    expect(transport.attachedProjectDescriptorCalls[0]?.request).toMatchObject({
      target: {
        target: {
          case: "newChat",
          value: {
            projectId: "project-1",
            workspaceId: "workspace-1",
            initialSettings: {
              agentRole: "default",
              thinking: "high",
              fast: true,
              questionsEnabled: false,
              autoCompactionEnabled: true,
            },
          },
        },
      },
      activation: {
        input: {
          case: "command",
          value: {
            catalogIdentity: "builtin:review",
            token: "/review",
            separatorWhitespace: "\t",
            arguments: "working tree",
          },
        },
      },
    });
    expect(transport.descriptorCalls).toHaveLength(1);
    expect(transport.runtimeOwnerRuns).toBe(0);
    expect(transport.attachedSessionCalls).toHaveLength(0);
    expect(transport.dedicatedCalls).toHaveLength(0);
  });

  it("Returns typed input non-acceptance as a domain result", async () => {
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatService.method.steer,
        result: create(ChatService.method.steer.output, {
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
    const client = new ApiClient(transport);

    await expect(client.chat.steer(sessionTarget, { kind: "text", text: "continue" })).resolves.toEqual({
      sessionID,
      outcome: { kind: "not_accepted", reason: { kind: "runtime_unavailable" } },
    });
  });

  it("Preserves accepted prompt-history diagnostics", async () => {
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatService.method.queue,
        result: create(ChatService.method.queue.output, {
          outcome: {
            case: "success",
            value: {
              session: { sessionId: sessionID },
              outcome: {
                case: "accepted",
                value: {
                  queueItem: { id: queueItemID },
                  diagnostic: {
                    detail: {
                      case: "promptHistoryFailure",
                      value: { operation: "history.record", cause: "disk full" },
                    },
                  },
                },
              },
            },
          },
        }),
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.chat.queue(sessionTarget, { kind: "text", text: "continue" })).resolves.toMatchObject(
      {
        outcome: {
          kind: "accepted",
          diagnostic: {
            kind: "prompt_history_failure",
            operation: "history.record",
            cause: "disk full",
          },
        },
      },
    );
  });

  it("Throws typed Chat operation errors with shared user-facing mapping", async () => {
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatService.method.steer,
        result: create(ChatService.method.steer.output, {
          outcome: {
            case: "error",
            value: {
              code: "session_not_found",
              detail: { case: "sessionNotFound", value: { sessionId: sessionID } },
            },
          },
        }),
      },
    ]);
    const client = new ApiClient(transport);

    const error = await client.chat
      .steer(sessionTarget, { kind: "text", text: "continue" })
      .catch((cause: unknown) => cause);
    expect(error).toBeInstanceOf(ChatOperationError);
    expect(error).toMatchObject({
      detail: { kind: "session_not_found", sessionID },
    });
    if (!(error instanceof ChatOperationError)) throw new Error("Expected typed Chat operation error.");
    const message = chatErrorMessage(error.detail);
    expect(typeof message).toBe("string");
    expect(message.length).toBeGreaterThan(0);
  });

  it("Projects Agent preparation failures into domain categories", async () => {
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatService.method.queue,
        result: create(ChatService.method.queue.output, {
          outcome: {
            case: "error",
            value: {
              code: "chat_settings_agent_preparation",
              detail: {
                case: "chatSettingsAgentPreparation",
                value: {
                  agent: "reviewer",
                  category: AgentPreparationCategory.PROVIDER_UNAVAILABLE,
                },
              },
            },
          },
        }),
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.chat.queue(sessionTarget, { kind: "text", text: "continue" })).rejects.toMatchObject({
      detail: {
        kind: "agent_preparation",
        agent: "reviewer",
        category: "provider_unavailable",
      },
    });
  });

  it("Preserves an unknown nonempty Chat operation code generically", async () => {
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatService.method.queue,
        result: create(ChatService.method.queue.output, {
          outcome: {
            case: "error",
            value: { code: "future_chat_failure" },
          },
        }),
      },
    ]);
    const client = new ApiClient(transport);

    const error = await client.chat
      .queue(sessionTarget, { kind: "text", text: "continue" })
      .catch((cause: unknown) => cause);
    expect(error).toBeInstanceOf(ChatOperationError);
    expect(error).toMatchObject({
      detail: { kind: "unknown", code: "future_chat_failure" },
    });
  });

  it("Preserves exact lexical compaction input and a fresh New Chat identity when too soon", async () => {
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatService.method.compact,
        result: create(ChatService.method.compact.output, {
          outcome: {
            case: "success",
            value: {
              session: { sessionId: sessionID },
              outcome: { case: "notAccepted", value: { reason: { case: "tooSoon", value: {} } } },
            },
          },
        }),
      },
    ]);
    const client = new ApiClient(transport);

    await expect(
      client.chat.compact(newChatTarget, {
        token: "/compact",
        separatorWhitespace: " \t",
        rawGuidance: " keep   decisions ",
      }),
    ).resolves.toEqual({
      sessionID,
      outcome: { kind: "not_accepted", reason: { kind: "too_soon" } },
    });
    expect(transport.attachedProjectDescriptorCalls[0]?.request).toMatchObject({
      invocation: {
        token: "/compact",
        separatorWhitespace: " \t",
        rawGuidance: " keep   decisions ",
      },
    });
  });

  it("Returns an accepted manual-compaction identity and diagnostic", async () => {
    const compactionRequestID = "323e4567-e89b-42d3-a456-426614174000";
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatService.method.compact,
        result: create(ChatService.method.compact.output, {
          outcome: {
            case: "success",
            value: {
              session: { sessionId: sessionID },
              outcome: {
                case: "accepted",
                value: {
                  request: { id: compactionRequestID },
                  diagnostic: {
                    detail: {
                      case: "internalFailure",
                      value: { operation: "history.record", cause: "disk full" },
                    },
                  },
                },
              },
            },
          },
        }),
      },
    ]);
    const client = new ApiClient(transport);

    const result = await client.chat.compact(sessionTarget, {
      token: "/compact",
      separatorWhitespace: "",
      rawGuidance: "",
    });
    expect(result.sessionID).toBe(sessionID);
    expect(result.outcome.kind).toBe("accepted");
    if (result.outcome.kind !== "accepted") throw new Error("Expected accepted compaction fixture.");
    expect(result.outcome.requestID.toJSONValue()).toBe(compactionRequestID);
    expect(result.outcome.diagnostic).toEqual({
      kind: "internal_failure",
      operation: "history.record",
      cause: "disk full",
    });
  });

  it("Maps exact-live Stop statuses to stopped and idle", async () => {
    const statuses = [
      LiveStopStatus.RUNTIME_LIVE_STOP_STATUS_STOPPED,
      LiveStopStatus.RUNTIME_LIVE_STOP_STATUS_IDLE,
    ] as const;
    const transport = new FakeRpcTransport([
      {
        descriptor: LiveService.method.stop,
        resultFactory: (_request, callIndex) => {
          const status = statuses[callIndex];
          if (status === undefined) throw new Error("Unexpected Stop fixture call.");
          return create(LiveService.method.stop.output, {
            outcome: { case: "success", value: { status } },
          });
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.chat.stop(sessionTarget)).resolves.toBe("stopped");
    await expect(client.chat.stop(sessionTarget)).resolves.toBe("idle");
    expect(transport.descriptorCalls.map((call) => call.descriptor)).toEqual([
      LiveService.method.stop,
      LiveService.method.stop,
    ]);
  });

  it("Resolves Edit as a fork and returns the child Session identity", async () => {
    const childSessionID = "323e4567-e89b-42d3-a456-426614174000";
    const transport = new FakeRpcTransport([
      {
        descriptor: SessionLifecycleService.method.resolveTransition,
        result: create(SessionLifecycleService.method.resolveTransition.output, {
          outcome: {
            case: "success",
            value: {
              directive: {
                case: "launch",
                value: {
                  intent: { intent: { case: "openExistingSessionId", value: childSessionID } },
                  preparation: {
                    inputPolicy: {
                      disposition: { case: "overrideStoredDraft", value: "original message" },
                    },
                    auth: SessionAuthPreparation.KEEP_CURRENT_AUTH,
                  },
                },
              },
            },
          },
        }),
      },
    ]);
    const client = new ApiClient(transport);

    await expect(
      client.chat.forkEdit(sessionTarget, {
        rollbackTargetID: "rollback-user-message-2",
        initialInput: "original message",
      }),
    ).resolves.toBe(childSessionID);
    expect(transport.attachedProjectDescriptorCalls[0]?.request).toMatchObject({
      sessionId: sessionID,
      transition: {
        action: SessionTransitionAction.FORK_ROLLBACK,
        forkRollbackTargetId: "rollback-user-message-2",
        initialInput: "original message",
      },
    });
  });

  it("Lists Pending Work as domain values", async () => {
    const transport = new FakeRpcTransport([
      {
        descriptor: TurnService.method.listPendingWork,
        result: create(TurnService.method.listPendingWork.output, {
          outcome: {
            case: "success",
            value: {
              pendingWork: {
                items: [
                  {
                    id: queueItemID,
                    lane: PendingWorkLane.QUEUE,
                    kind: PendingWorkItemKind.MESSAGE,
                    payload: { case: "message", value: { text: "queued" } },
                    state: PendingWorkItemState.PENDING,
                    canonicalInput: "queued",
                  },
                ],
              },
            },
          },
        }),
      },
    ]);
    const client = new ApiClient(transport);

    const result = await client.chat.listPendingWork(sessionTarget);
    expect(result.items).toHaveLength(1);
    expect(result.items[0]).toMatchObject({
      lane: "queue",
      kind: "message",
      state: "pending",
      canonicalInput: "queued",
      message: { text: "queued" },
    });
    expect(result.items[0]?.id.toJSONValue()).toBe(queueItemID);
  });

  it("Removes Pending Work and returns the restoration domain value", async () => {
    const transport = new FakeRpcTransport([
      {
        descriptor: TurnService.method.removePendingWork,
        result: create(TurnService.method.removePendingWork.output, {
          outcome: {
            case: "success",
            value: {
              restoration: {
                kind: PendingWorkItemKind.MESSAGE,
                canonicalInput: "queued",
              },
            },
          },
        }),
      },
    ]);
    const client = new ApiClient(transport);
    const brandedID = parsePendingWorkItemID(queueItemID);
    await expect(client.chat.removePendingWork(sessionTarget, brandedID)).resolves.toEqual({
      kind: "message",
      canonicalInput: "queued",
    });
    expect(transport.attachedProjectDescriptorCalls[0]?.request).toMatchObject({
      sessionId: sessionID,
      itemId: queueItemID,
    });
  });

  it("Uses the shared typed Chat error for Pending Work failures", async () => {
    const transport = new FakeRpcTransport([
      {
        descriptor: TurnService.method.listPendingWork,
        result: create(TurnService.method.listPendingWork.output, {
          outcome: {
            case: "error",
            value: {
              code: "runtime_unavailable",
              detail: { case: "runtimeUnavailable", value: { sessionId: sessionID } },
            },
          },
        }),
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.chat.listPendingWork(sessionTarget)).rejects.toMatchObject({
      name: "ChatOperationError",
      detail: { kind: "runtime_unavailable", sessionID },
    });
  });

  it("Rejects a mutation response for a different existing Session", async () => {
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatService.method.steer,
        result: create(ChatService.method.steer.output, {
          outcome: {
            case: "success",
            value: {
              session: { sessionId: "423e4567-e89b-42d3-a456-426614174000" },
              outcome: {
                case: "accepted",
                value: { queueItem: { id: queueItemID } },
              },
            },
          },
        }),
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.chat.steer(sessionTarget, { kind: "text", text: "continue" })).rejects.toBeInstanceOf(
      ContractError,
    );
  });

  it("Rejects malformed accepted identities before domain projection", async () => {
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatService.method.steer,
        result: create(ChatService.method.steer.output, {
          outcome: {
            case: "success",
            value: {
              session: { sessionId: sessionID },
              outcome: {
                case: "accepted",
                value: { queueItem: { id: "not-a-queue-item-id" } },
              },
            },
          },
        }),
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.chat.steer(sessionTarget, { kind: "text", text: "continue" })).rejects.toBeInstanceOf(
      Error,
    );
  });

  it("Propagates disconnected mutation transport failures without replay", async () => {
    const failure = new Error("connection closed");
    const transport = new FakeRpcTransport([{ descriptor: ChatService.method.queue, error: failure }]);
    const client = new ApiClient(transport);

    await expect(client.chat.queue(sessionTarget, { kind: "text", text: "continue" })).rejects.toBe(failure);
    expect(transport.descriptorCalls).toHaveLength(1);
  });
});
