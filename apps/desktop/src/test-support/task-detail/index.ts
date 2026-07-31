import { z } from "zod";
import { createElement } from "react";
import { render } from "@testing-library/react";

import { guiTaskCommentAuthor, type JsonObject, type JsonValue, type TaskDetail } from "@/api";
import { ApiClient } from "@/api/composition";
import { TaskDetailSurface } from "@/features/task-detail";
import { FakeRpcTransport, type FakeRoute } from "../api";
import { createTestServices, startupRoutes, TestAppProviders, type TestAppServices } from "../app-services";
import type { NativeBridge } from "../native-bridge";

const jsonObjectSchema = z.record(z.string(), z.unknown());

export const taskUpdateParamsSchema = jsonObjectSchema.and(
  z.object({
    body: z.string().optional(),
    title: z.string().optional(),
  }),
);

const workflow = {
  workflow_id: "11111111-1111-4111-8111-111111111111",
  display_name: "Delivery",
  version: 1,
};

const workspace = {
  workspace_id: "workspace-1",
  display_name: "Main",
  root_path: "/tmp/project",
  availability: "available",
  is_primary: true,
  updated_at_unix_ms: 1,
};

const taskActions = {
  can_start: false,
  can_interrupt: true,
  can_resume: false,
  can_delete: false,
};

const attentionBase = {
  project_id: "project-1",
  workflow_id: "11111111-1111-4111-8111-111111111111",
  task_id: "task-1",
  task_short_id: "T-1",
  task_title: "Resolve blocker",
  occurred_at_unix_ms: 1,
};

export const taskDetailResponse = {
  task: {
    summary: {
      id: "task-1",
      project_id: "project-1",
      workflow_id: "11111111-1111-4111-8111-111111111111",
      short_id: "T-1",
      title: "Resolve blocker",
      created_at_unix_ms: 1,
      updated_at_unix_ms: 2,
      done: false,
    },
    project: { display_name: "Project" },
    workflow,
    body: "Need operator input",
    source_workspace: workspace,
    execution_target: {
      mode: "head",
      requested_ref: "HEAD",
      resolved_ref: "refs/heads/main",
      commit_oid: "0123456789abcdef0123456789abcdef01234567",
      provenance: "resolved",
    },
    worktree_path: "/tmp/worktree",
    current_nodes: [
      {
        node_id: "node-1",
        transition_branch_key: null,
        session_id: "session-1",
      },
    ],
    live_session_ids: ["session-1"],
    current_scripts: [],
    retained_session_count: 1,
    status: {
      kind: "running",
      native_state: "running",
      node_ids: ["node-1"],
      attention_types: ["question", "approval"],
    },
    actions: taskActions,
    label_ids: [],
    attention_count: 2,
  },
};

export const taskAttentionResponse = {
  items: [
    {
      ...attentionBase,
      id: "attention-question",
      kind: "question",
      current_node: {
        node_id: "node-1",
        transition_branch_key: null,
        session_id: "session-1",
      },
      session_id: "session-1",
      question_id: "ask-1",
      message: "Approve protected path?",
    },
    {
      ...attentionBase,
      id: "attention-approval",
      kind: "approval",
      approval_id: "approval-1",
      approval_snapshot: {
        source_node_display_name: "Implement",
        targets: [{ display_name: "Ship" }],
        commentary: "Looks good",
        output_values: { result: "ok" },
        workflow_revision_seen: 7,
      },
    },
  ],
  generated_at_unix_ms: 3,
};

export const emptyTaskAttentionResponse = {
  items: [],
  generated_at_unix_ms: 3,
};

export async function createTaskDetailFixture(): Promise<TaskDetail> {
  const client = new ApiClient(
    new FakeRpcTransport([{ method: "workflow.task.get", result: taskDetailResponse }]),
  );
  return client.getTask("task-1");
}

export const taskDetailResponseWithAdditionalLiveSession = {
  task: {
    ...taskDetailResponse.task,
    live_session_ids: ["session-1", "session-2"],
  },
};

export const taskDetailNoInboxResponse = {
  task: {
    ...taskDetailResponse.task,
    attention_count: 0,
  },
};

export const taskDetailResponseWithCurrentScript = {
  task: {
    ...taskDetailNoInboxResponse.task,
    current_nodes: [{ node_id: "node-script", transition_branch_key: null, session_id: null }],
    live_session_ids: [],
    current_scripts: [
      {
        current_node: { node_id: "node-script", transition_branch_key: null, session_id: null },
        path: "scripts/run",
      },
    ],
  },
};

export const taskDetailResponseWithInterruptedCurrentScript = {
  task: {
    ...taskDetailResponseWithCurrentScript.task,
    actions: { ...taskActions, can_interrupt: false, can_resume: true },
    attention_count: 1,
    current_scripts: [],
  },
};

export const interruptedTaskAttentionResponse = {
  items: [
    {
      ...attentionBase,
      id: "attention-interrupted",
      kind: "interrupted_current_node",
      current_node: { node_id: "node-script", transition_branch_key: null, session_id: null },
      session_id: null,
      detail_json: '{"kind":"script_failure","stderr":"permission denied"}',
    },
  ],
  generated_at_unix_ms: 3,
};

export const questionAttention = {
  ...attentionBase,
  id: "attention-question",
  kind: "question",
  current_node: {
    node_id: "node-1",
    transition_branch_key: null,
    session_id: "session-1",
  },
  session_id: "session-1",
  question_id: "ask-1",
  message: "Choose snack",
  recommended_option_index: 1,
  suggestions: ["Trail mix", "Dark chocolate"],
};

export const taskQuestionWaitingEvent = {
  event: {
    resource: "task",
    action: "question_waiting",
    occurred_at_unix_ms: 1,
    primary_entity_id: "task-1",
    project_id: "project-1",
    related_ids: ["session-1", "ask-1"],
    workflow_id: "11111111-1111-4111-8111-111111111111",
  },
};

export const taskUpdatedEvent = {
  event: {
    resource: "task",
    action: "updated",
    occurred_at_unix_ms: 1,
    primary_entity_id: "task-1",
    project_id: "project-1",
    workflow_id: "11111111-1111-4111-8111-111111111111",
  },
};

export const activityResponse = {
  items: [
    {
      activity_id: "activity-1",
      type: "comment",
      task_id: "task-1",
      occurred_at_unix_ms: 2,
      updated_at_unix_ms: 2,
      comment: {
        id: "comment-activity-1",
        task_id: "task-1",
        body: "Comment added",
        author: "user",
        created_at_unix_ms: 2,
        updated_at_unix_ms: 2,
      },
    },
  ],
  next_page_token: "",
  generated_at_unix_ms: 3,
};

export const pendingAskResponse = {
  Asks: [
    {
      AskID: "ask-1",
      SessionID: "session-1",
      Question: "Choose path",
      Suggestions: ["Use option A", "Use option B"],
      RecommendedOptionIndex: 1,
      CreatedAt: "2026-05-16T00:00:00Z",
    },
  ],
};

export const commentAddResponse = {
  comment: {
    id: "comment-2",
    task_id: "task-1",
    body: "Fresh comment",
    author: guiTaskCommentAuthor,
    created_at_unix_ms: 4,
    updated_at_unix_ms: 4,
  },
};

export const commentListResponse = {
  comments: [
    {
      id: "comment-1",
      task_id: "task-1",
      body: "Existing comment",
      author: guiTaskCommentAuthor,
      created_at_unix_ms: 1,
      updated_at_unix_ms: 1,
    },
  ],
  next_page_token: "",
};

export const firstCommentListResponse = {
  comments: [
    {
      id: "comment-page-1",
      task_id: "task-1",
      body: "First paged comment",
      author: guiTaskCommentAuthor,
      created_at_unix_ms: 5,
      updated_at_unix_ms: 5,
    },
  ],
  next_page_token: "cursor-2",
};

export const secondCommentListResponse = {
  comments: [
    {
      id: "comment-page-2",
      task_id: "task-1",
      body: "Second paged comment",
      author: guiTaskCommentAuthor,
      created_at_unix_ms: 6,
      updated_at_unix_ms: 6,
    },
  ],
  next_page_token: "",
};

export const taskUpdateResponse = {
  task: {
    id: "task-1",
  },
};

export type TaskDetailFixtureOptions = Readonly<{
  attention?: JsonValue;
  asks?: unknown;
  comments?: unknown;
  nativeBridge?: NativeBridge | undefined;
  path?: string | undefined;
  routes?: readonly FakeRoute[] | undefined;
}>;

export function createTaskDetailTestServices(
  task: JsonValue,
  {
    asks,
    attention = taskAttentionFixture(task),
    comments,
    nativeBridge,
    path = "/tasks/task-1",
    routes = [],
  }: TaskDetailFixtureOptions = {},
): TestAppServices {
  window.history.pushState(null, "", path);
  return createTestServices(
    [
      ...startupRoutes,
      {
        method: "workflow.project.label.list",
        result: {
          catalog: {
            project_id: "project-1",
            labels: [],
          },
        },
      },
      { method: "workflow.task.get", result: task },
      { method: "workflow.task.attention.list", result: attention },
      ...(comments === undefined ? [] : [{ method: "workflow.task.comment.list", result: comments }]),
      { method: "workflow.task.activity.list", result: activityResponse },
      ...(asks === undefined ? [] : [{ method: "ask.listPendingBySession", result: asks }]),
      ...routes,
    ],
    nativeBridge,
  );
}

export function mountTaskDetailSurface(
  task: JsonValue,
  options: TaskDetailFixtureOptions = {},
): TestAppServices {
  const services = createTaskDetailTestServices(task, options);
  render(
    createElement(TestAppProviders, {
      children: createElement(TaskDetailSurface, { enabled: true, taskId: "task-1" }),
      services,
    }),
  );
  return services;
}

function taskAttentionFixture(task: JsonValue): JsonValue {
  if (task === taskDetailResponseWithInterruptedCurrentScript) {
    return interruptedTaskAttentionResponse;
  }
  if (isJsonObject(task) && isJsonObject(task.task) && task.task.attention_count === 0) {
    return emptyTaskAttentionResponse;
  }
  return taskAttentionResponse;
}

export function callParams(
  calls: readonly Readonly<{ method: string; params: JsonValue }>[],
  method: string,
): JsonObject {
  const params = calls.find((call) => call.method === method)?.params;
  if (!isJsonObject(params)) {
    throw new Error(`Missing object params for ${method}.`);
  }
  return params;
}

export function getCallCount(
  calls: readonly Readonly<{ method: string; params: JsonValue }>[],
  method: string,
): number {
  return calls.filter((call) => call.method === method).length;
}

export function isJsonObject(value: JsonValue | undefined): value is JsonObject {
  return jsonObjectSchema.safeParse(value).success && !Array.isArray(value);
}
