import { create } from "@app/server-api-contract";
import { ChatService } from "@app/server-api-contract/gen/kent/api/chat/chat_pb";
import { AgentPreparationCategory } from "@app/server-api-contract/gen/kent/api/chat_settings/chat_settings_pb";

import { FakeRpcTransport } from "@/test-support/api";

import { requireProjectAttachment } from "./chatAttachment";
import { ChatOperationError, chatErrorMessage } from "./chatErrors";
import { ApiClient } from "./client";
import { ContractError } from "./errors";

const sessionID = "123e4567-e89b-42d3-a456-426614174000";
const queueItemID = "223e4567-e89b-42d3-a456-426614174000";
const compactionRequestID = "323e4567-e89b-42d3-a456-426614174000";
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

describe("Desktop Chat mutation adapter", () => {
  it("constructs representative Session and New Chat requests", async () => {
    const transport = new FakeRpcTransport([
      {
        descriptor: ChatService.method.steer,
        result: create(ChatService.method.steer.output, {
          outcome: {
            case: "success",
            value: {
              session: { sessionId: sessionID },
              outcome: { case: "accepted", value: { queueItem: { id: queueItemID } } },
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
              outcome: { case: "accepted", value: { queueItem: { id: queueItemID } } },
            },
          },
        }),
      },
    ]);
    const chat = new ApiClient(transport).chat;

    await chat.steer(sessionTarget, { kind: "text", text: "continue" });
    await chat.queue(newChatTarget, {
      kind: "command",
      catalogIdentity: "builtin:review",
      token: "/review",
      separatorWhitespace: "\t",
      arguments: "working tree",
    });

    expect(transport.attachedProjectDescriptorCalls.map(({ request }) => request)).toMatchObject([
      {
        target: { target: { case: "session", value: { sessionId: sessionID } } },
        activation: { input: { case: "text", value: "continue" } },
      },
      {
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
      },
    ]);
  });

  it("preserves exact compaction lexical fields and projects a rejection with its created Session", async () => {
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
    const chat = new ApiClient(transport).chat;

    await expect(
      chat.compact(newChatTarget, {
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

  it("parses accepted Queue and Compaction identities into domain values", async () => {
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
      {
        descriptor: ChatService.method.compact,
        result: create(ChatService.method.compact.output, {
          outcome: {
            case: "success",
            value: {
              session: { sessionId: sessionID },
              outcome: {
                case: "accepted",
                value: { request: { id: compactionRequestID } },
              },
            },
          },
        }),
      },
    ]);
    const chat = new ApiClient(transport).chat;

    const queued = await chat.queue(sessionTarget, { kind: "text", text: "continue" });
    expect(queued).toMatchObject({
      sessionID,
      outcome: {
        kind: "accepted",
        diagnostic: {
          kind: "prompt_history_failure",
          operation: "history.record",
          cause: "disk full",
        },
      },
    });
    if (queued.outcome.kind !== "accepted") throw new Error("Expected accepted Queue fixture.");
    expect(queued.outcome.queueItemID.toJSONValue()).toBe(queueItemID);

    const compacted = await chat.compact(sessionTarget, {
      token: "/compact",
      separatorWhitespace: "",
      rawGuidance: "",
    });
    if (compacted.outcome.kind !== "accepted") throw new Error("Expected accepted compaction fixture.");
    expect(compacted.outcome.requestID.toJSONValue()).toBe(compactionRequestID);
  });

  it("projects shared typed operation errors", async () => {
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
    const chat = new ApiClient(transport).chat;

    const error = await chat
      .queue(sessionTarget, { kind: "text", text: "continue" })
      .catch((cause: unknown) => cause);
    expect(error).toBeInstanceOf(ChatOperationError);
    expect(error).toMatchObject({
      detail: {
        kind: "agent_preparation",
        agent: "reviewer",
        category: "provider_unavailable",
      },
    });
    if (!(error instanceof ChatOperationError)) throw new Error("Expected typed Chat operation error.");
    expect(chatErrorMessage(error.detail).length).toBeGreaterThan(0);
  });

  it("rejects mismatched attachments, returned Sessions, and malformed identities", async () => {
    expect(() =>
      requireProjectAttachment(
        {
          projectID: "other-project",
          workspaceID: "workspace-1",
          workspaceRoot: "/workspace",
          workspaceSelection: { kind: "workspaceID", workspaceID: "workspace-1" },
        },
        sessionTarget,
      ),
    ).toThrow(ContractError);

    const cases = [
      {
        name: "returned Session",
        returnedSessionID: "423e4567-e89b-42d3-a456-426614174000",
        returnedQueueItemID: queueItemID,
      },
      {
        name: "Queue Item identity",
        returnedSessionID: sessionID,
        returnedQueueItemID: "not-a-queue-item-id",
      },
    ] as const;
    for (const testCase of cases) {
      const transport = new FakeRpcTransport([
        {
          descriptor: ChatService.method.steer,
          result: create(ChatService.method.steer.output, {
            outcome: {
              case: "success",
              value: {
                session: { sessionId: testCase.returnedSessionID },
                outcome: {
                  case: "accepted",
                  value: { queueItem: { id: testCase.returnedQueueItemID } },
                },
              },
            },
          }),
        },
      ]);
      await expect(
        new ApiClient(transport).chat.steer(sessionTarget, { kind: "text", text: testCase.name }),
      ).rejects.toBeInstanceOf(Error);
    }
  });
});
