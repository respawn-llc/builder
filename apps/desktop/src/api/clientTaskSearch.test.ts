import { FakeRpcTransport } from "@/test-support/api";

import { ApiClient } from "./client";
import {
  ContractError,
  decodeTaskSearchError,
  RpcError,
  TaskSearchError,
  type TaskSearchInput,
} from "./index";

const literalResponse = {
  mode: "literal",
  groups: [
    {
      project_id: "project-1",
      project_key: "KNT",
      task_id: "task-1",
      short_id: "KNT-1",
      workflow_id: "workflow-1",
      title: "Search tasks",
      status: {
        kind: "active",
        native_state: "active",
        node_ids: ["node-1"],
        attention_types: [],
      },
      total_hit_count: 2,
      hits: [
        {
          ordinal: 1,
          source: { kind: "title" },
          literal: {
            before: "",
            match: "Search",
            after: " tasks",
            left_truncated: false,
            right_truncated: false,
          },
        },
        {
          ordinal: 2,
          source: { kind: "comment", comment_id: "comment-1" },
          literal: {
            before: "Please ",
            match: "search",
            after: " this",
            left_truncated: true,
            right_truncated: true,
          },
        },
      ],
    },
  ],
  next_offset: 25,
} as const;
const literalGroup = literalResponse.groups[0];
const firstLiteralHit = literalGroup.hits[0];
const secondLiteralHit = literalGroup.hits[1];

const literalInput: TaskSearchInput = {
  mode: "literal",
  query: "search",
  context: 20,
  caseSensitive: false,
  includeComments: true,
  projectIDs: ["project-1", "project-2"],
  pageSize: 25,
  offset: 5,
};

describe("ApiClient task search", () => {
  it("sends the exact literal search payload and maps the grouped response", async () => {
    const transport = new FakeRpcTransport([{ method: "workflow.task.search", result: literalResponse }]);
    const client = new ApiClient(transport);
    const controller = new AbortController();

    await expect(client.searchTasks(literalInput, controller.signal)).resolves.toEqual({
      mode: "literal",
      groups: [
        {
          projectID: "project-1",
          projectKey: "KNT",
          taskID: "task-1",
          shortID: "KNT-1",
          workflowID: "workflow-1",
          title: "Search tasks",
          status: {
            kind: "active",
            nativeState: "active",
            nodeIDs: ["node-1"],
            attentionTypes: [],
          },
          totalHitCount: 2,
          hits: [
            {
              ordinal: 1,
              source: { kind: "title" },
              literal: {
                before: "",
                match: "Search",
                after: " tasks",
                leftTruncated: false,
                rightTruncated: false,
              },
            },
            {
              ordinal: 2,
              source: { kind: "comment", commentID: "comment-1" },
              literal: {
                before: "Please ",
                match: "search",
                after: " this",
                leftTruncated: true,
                rightTruncated: true,
              },
            },
          ],
        },
      ],
      nextOffset: 25,
    });

    expect(transport.calls).toEqual([]);
    expect(transport.dedicatedCalls).toEqual([
      {
        method: "workflow.task.search",
        params: {
          mode: "literal",
          query: "search",
          context: 20,
          case_sensitive: false,
          include_comments: true,
          project_ids: ["project-1", "project-2"],
          page_size: 25,
          offset: 5,
        },
        options: { signal: controller.signal },
      },
    ]);
  });

  it("preserves an explicit null next offset as absent pagination", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([
        {
          method: "workflow.task.search",
          result: { ...literalResponse, next_offset: null },
        },
      ]),
    );

    await expect(client.searchTasks(literalInput)).resolves.toMatchObject({
      mode: "literal",
      nextOffset: null,
    });
  });

  it.each([
    {
      name: "an unexpected response field",
      response: { ...literalResponse, legacy_hits: [] },
    },
    {
      name: "a mode-incompatible hit payload",
      response: {
        ...literalResponse,
        groups: [
          {
            ...literalResponse.groups[0],
            hits: [
              {
                ordinal: 1,
                source: { kind: "title" },
                fts5: { snippet: "search" },
              },
            ],
          },
        ],
      },
    },
    {
      name: "a non-canonical task status",
      response: {
        ...literalResponse,
        groups: [
          {
            ...literalResponse.groups[0],
            status: { ...literalGroup.status, native_state: "running" },
          },
        ],
      },
    },
    {
      name: "unordered hit ordinals",
      response: {
        ...literalResponse,
        groups: [
          {
            ...literalResponse.groups[0],
            hits: [secondLiteralHit, firstLiteralHit],
          },
        ],
      },
    },
  ])("rejects $name", async ({ response }) => {
    const client = new ApiClient(
      new FakeRpcTransport([{ method: "workflow.task.search", result: response }]),
    );

    await expect(client.searchTasks(literalInput)).rejects.toBeInstanceOf(ContractError);
  });

  it("surfaces normalized-too-short as a typed error and omits absent request fields", async () => {
    const typedError = new RpcError({
      code: -32052,
      message: "display-only search failure",
      method: "workflow.task.search",
      data: {
        type: "task_search_error",
        reason: "normalized_too_short",
      },
    });
    const transport = new FakeRpcTransport([{ method: "workflow.task.search", error: typedError }]);
    const client = new ApiClient(transport);

    await expect(
      client.searchTasks({
        mode: "literal",
        query: "ab",
        context: 20,
        caseSensitive: false,
        includeComments: true,
        projectIDs: ["project-1"],
        pageSize: 10,
      }),
    ).rejects.toMatchObject({
      reason: "normalized_too_short",
      code: -32052,
      method: "workflow.task.search",
    });
    expect(transport.calls).toEqual([]);
    expect(transport.dedicatedCalls).toEqual([
      {
        method: "workflow.task.search",
        params: {
          mode: "literal",
          query: "ab",
          context: 20,
          case_sensitive: false,
          include_comments: true,
          project_ids: ["project-1"],
          page_size: 10,
        },
      },
    ]);

    const decoded = decodeTaskSearchError(typedError);
    expect(decoded).toBeInstanceOf(TaskSearchError);
    expect(decoded).toMatchObject({
      reason: "normalized_too_short",
      code: -32052,
      method: "workflow.task.search",
      message: "display-only search failure",
    });
  });

  it.each([
    new RpcError({
      code: -32052,
      message: "generic search failure",
      method: "workflow.task.search",
    }),
    new RpcError({
      code: -32052,
      message: "unknown typed search failure",
      method: "workflow.task.search",
      data: { type: "task_search_error", reason: "other" },
    }),
    new RpcError({
      code: -32000,
      message: "wrong RPC code",
      method: "workflow.task.search",
      data: { type: "task_search_error", reason: "normalized_too_short" },
    }),
  ])("keeps generic or malformed RPC failures visible", async (error) => {
    const client = new ApiClient(new FakeRpcTransport([{ method: "workflow.task.search", error }]));

    await expect(client.searchTasks(literalInput)).rejects.toBe(error);
    expect(decodeTaskSearchError(error)).toBeNull();
  });
});
