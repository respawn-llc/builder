import { guiTaskCommentAuthor } from "../../api/client";

const workflow = {
  workflow_id: "workflow-1",
  display_name: "Delivery",
  description: "",
  version: 1,
  is_project_default: true,
  valid_for_task_creation: true,
  validation_errors: [],
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
  can_cancel: true,
};

const attentionBase = {
  project_id: "project-1",
  workflow_id: "workflow-1",
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
      workflow_id: "workflow-1",
      short_id: "T-1",
      title: "Resolve blocker",
      created_at_unix_ms: 1,
      updated_at_unix_ms: 2,
      done: false,
      canceled_at_unix_ms: 0,
    },
    project: { display_name: "Project" },
    workflow,
    body: "Need operator input",
    source_workspace: workspace,
    managed_worktree: { canonical_root: "/tmp/worktree" },
    status: {
      kind: "running",
      label: "Running",
      native_state: "running",
      node_ids: ["node-1"],
      run_ids: ["run-1"],
      attention_types: ["question", "approval"],
    },
    actions: taskActions,
    attention: [
      {
        ...attentionBase,
        id: "attention-question",
        kind: "question",
        run_id: "run-1",
        session_id: "session-1",
        ask_id: "ask-1",
        task_transition_id: "",
        message: "",
      },
      {
        ...attentionBase,
        id: "attention-approval",
        kind: "approval",
        run_id: "run-1",
        session_id: "session-1",
        ask_id: "",
        task_transition_id: "transition-1",
        message: "Approve transition",
      },
    ],
    runs: [
      {
        id: "run-1",
        task_id: "task-1",
        placement_id: "placement-1",
        node_id: "node-1",
        session_id: "session-1",
        session_name: "Kent session",
        role: "agent",
        status: "running",
        generation: 1,
        waiting_ask_id: "ask-1",
        started_at_unix_ms: 1,
        completed_at_unix_ms: 0,
        interrupted_at_unix_ms: 0,
      },
    ],
    transitions: [
      {
        id: "transition-1",
        transition_id: "ship",
        transition_display_name: "Ship",
        source_node_display_name: "Implement",
        state: "pending_approval",
        commentary: "Looks good",
        output_values: { result: "ok" },
        edges: [
          {
            id: "transition-edge-1",
            edge_key: "ship",
            target_node_display_name: "Ship",
            state: "pending",
            requires_approval: true,
            output_requirements: [],
          },
        ],
        workflow_revision_seen: 7,
        created_at_unix_ms: 2,
        applied_at_unix_ms: 0,
      },
    ],
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
  },
};

export const taskDetailResponseWithNewerActiveRun = {
  task: {
    ...taskDetailResponse.task,
    runs: [
      ...taskDetailResponse.task.runs,
      {
        ...taskDetailResponse.task.runs[0],
        id: "run-2",
        session_id: "session-2",
        started_at_unix_ms: 2,
      },
    ],
  },
};

export const taskDetailNoInboxResponse = {
  task: {
    ...taskDetailResponse.task,
    attention: [],
    transitions: [],
  },
};

export const taskDetailResponseWithScriptRun = {
  task: {
    ...taskDetailNoInboxResponse.task,
    actions: { ...taskActions, can_interrupt: false },
    runs: [
      {
        id: "run-script",
        task_id: "task-1",
        placement_id: "placement-script",
        node_id: "node-script",
        node_kind: "script",
        script_path: "scripts/run",
        session_id: "",
        session_name: "",
        role: "script",
        status: "interrupted",
        generation: 1,
        waiting_ask_id: "",
        started_at_unix_ms: 1,
        completed_at_unix_ms: 0,
        interrupted_at_unix_ms: 2,
        interruption_reason: "script failed",
        interruption_detail_json: '{"kind":"script_failure"}',
      },
    ],
  },
};

export const taskDetailResponseWithInterruptedScriptRun = {
  task: {
    ...taskDetailResponseWithScriptRun.task,
    actions: { ...taskActions, can_interrupt: false, can_resume: true },
    attention: [
      {
        ...attentionBase,
        id: "attention-interrupted",
        kind: "interrupted_run",
        run_id: "run-script",
        session_id: "",
        ask_id: "",
        task_transition_id: "",
        message: "Script failed",
        detail_json: '{"kind":"script_failure","stderr":"permission denied"}',
      },
    ],
  },
};

export const questionAttention = {
  ...attentionBase,
  id: "attention-question",
  kind: "question",
  run_id: "run-1",
  session_id: "session-1",
  ask_id: "ask-1",
  task_transition_id: "",
  message: "Choose snack",
  recommended_option_index: 1,
  suggestions: ["Trail mix", "Dark chocolate"],
};

export const taskQuestionWaitingEvent = {
  event: {
    resource: "task",
    action: "question_waiting",
    changed_ids: ["task-1", "run-1", "ask-1"],
    project_id: "project-1",
    workflow_id: "workflow-1",
  },
};

export const taskUpdatedEvent = {
  event: {
    resource: "task",
    action: "updated",
    changed_ids: ["task-1"],
    project_id: "project-1",
    workflow_id: "workflow-1",
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
      actor: "GUI",
      summary: "Comment added",
      comment: null,
      transition: null,
      run: null,
      attention: null,
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
  comments: taskDetailResponse.task.comments,
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
