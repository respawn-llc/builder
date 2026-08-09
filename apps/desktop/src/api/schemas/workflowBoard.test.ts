import {
  activityPageSchema,
  boardNodeCardsPageSchema,
  pendingAskListSchema,
  taskMovePreviewResponseSchema,
  workflowBoardSchema,
} from "./workflowBoard";

const workspace = {
  workspace_id: "workspace-default",
  display_name: "Default workspace",
  root_path: "/workspace/default",
  availability: "available",
  is_primary: true,
  updated_at_unix_ms: 1,
};

const selectedWorkflow = {
  workflow_id: "11111111-1111-4111-8111-111111111111",
  display_name: "Workflow",
  description: "",
  version: 1,
  is_project_default: true,
  valid_for_task_creation: true,
  validation_errors: [],
};

const boardResponse = {
  board: {
    project_id: "project-1",
    project: {
      project_key: "KNT",
      display_name: "Kent",
      default_workspace_id: "workspace-default",
      attached_workspace_count: 2,
    },
    selected_workflow: selectedWorkflow,
    workflows: [selectedWorkflow],
    groups: [],
    columns: [],
    generated_at_unix_ms: 1,
  },
};

const card = {
  task_id: "task-1",
  short_id: "KNT-1",
  title: "Task",
  preview: {
    markdown: "Bounded Markdown **preview**",
    truncated: true,
  },
  workflow_id: "11111111-1111-4111-8111-111111111111",
  active_node_ids: [],
  source_workspace: workspace,
  status: {
    kind: "backlog",
    native_state: "active",
    node_ids: [],
    attention_types: [],
  },
  actions: {
    can_start: true,
    can_interrupt: false,
    can_resume: false,
    can_delete: true,
  },
  label_ids: ["f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf"],
  updated_at_unix_ms: 1,
};

describe("workflow board schemas", () => {
  it("preserves pending-ask recommendation presence and rejects invalid indexes", () => {
    const pendingAsk = {
      PromptID: "ask-1",
      SessionID: "session-1",
      StepID: "11111111-1111-4111-8111-111111111111",
      Question: "Choose?",
      Suggestions: ["one", "two"],
      CreatedAt: "2026-08-03T00:00:00Z",
    };
    expect(
      pendingAskListSchema.parse({
        Asks: [{ ...pendingAsk, RecommendedOptionIndex: null }],
      })[0]?.recommendedOptionIndex,
    ).toBeNull();
    expect(
      pendingAskListSchema.parse({
        Asks: [{ ...pendingAsk, RecommendedOptionIndex: 2 }],
      })[0]?.recommendedOptionIndex,
    ).toBe(2);
    expect(() =>
      pendingAskListSchema.parse({
        Asks: [{ ...pendingAsk, RecommendedOptionIndex: 3 }],
      }),
    ).toThrow();
  });

  it("decodes required parent workspace facts", () => {
    expect(workflowBoardSchema.parse(boardResponse)).toMatchObject({
      defaultWorkspaceID: "workspace-default",
      attachedWorkspaceCount: 2,
    });
  });

  it("rejects card and pagination fields on board metadata", () => {
    expect(() =>
      workflowBoardSchema.parse({
        board: {
          ...boardResponse.board,
          cards: [],
          done_preview: [],
          has_hidden_done_cards: false,
          next_page_token: null,
        },
      }),
    ).toThrow();
  });

  it("decodes omitted and null board workflow selection as absence", () => {
    const omittedSelection = structuredClone(boardResponse);
    Reflect.deleteProperty(omittedSelection.board, "selected_workflow");
    expect(workflowBoardSchema.parse(omittedSelection)).toMatchObject({
      selectedWorkflow: null,
    });

    expect(
      workflowBoardSchema.parse({
        board: {
          ...boardResponse.board,
          selected_workflow: null,
        },
      }),
    ).toMatchObject({ selectedWorkflow: null });
  });

  it("preserves whitespace in resolved Manual Move values", () => {
    const resolvedValue = "  indented code\n ";
    const parsed = taskMovePreviewResponseSchema.parse({
      outcome: "transition",
      transition: {
        choices: [
          {
            transition_key: "next",
            label: "Next",
            source_node_display_name: "Plan",
            required_values: [
              {
                node_key: "plan",
                output_name: "summary",
                description: "Summary",
                resolved_value: resolvedValue,
              },
            ],
          },
        ],
      },
    });

    expect(parsed).toMatchObject({
      transition: {
        choices: [
          {
            requiredValues: [{ resolvedValue }],
          },
        ],
      },
    });
  });

  it("rejects legacy prefixed Workflow IDs", () => {
    expect(() =>
      workflowBoardSchema.parse({
        board: {
          ...boardResponse.board,
          selected_workflow: {
            ...selectedWorkflow,
            workflow_id: "workflow-11111111-1111-4111-8111-111111111111",
          },
        },
      }),
    ).toThrow();
  });

  it("rejects a present board workflow selection with a blank ID", () => {
    expect(() =>
      workflowBoardSchema.parse({
        board: {
          ...boardResponse.board,
          selected_workflow: {
            ...selectedWorkflow,
            workflow_id: " ",
          },
        },
      }),
    ).toThrow();
  });

  it("rejects missing or invalid parent workspace facts", () => {
    const projectWithoutDefaultWorkspaceID = {
      project_key: boardResponse.board.project.project_key,
      display_name: boardResponse.board.project.display_name,
      attached_workspace_count: boardResponse.board.project.attached_workspace_count,
    };
    expect(() =>
      workflowBoardSchema.parse({
        board: {
          ...boardResponse.board,
          project: projectWithoutDefaultWorkspaceID,
        },
      }),
    ).toThrow();

    const invalidAttachedWorkspaceCount = structuredClone(boardResponse);
    invalidAttachedWorkspaceCount.board.project.attached_workspace_count = -1;
    expect(() => workflowBoardSchema.parse(invalidAttachedWorkspaceCount)).toThrow();
  });

  it("decodes nested Markdown previews, nullable cursors, and canonical detached workspace availability", () => {
    const page = boardNodeCardsPageSchema.parse({
      project_id: "project-1",
      workflow_id: "11111111-1111-4111-8111-111111111111",
      node_id: "node-1",
      cards: [
        {
          ...card,
          source_workspace: {
            ...workspace,
            availability: "unlinked",
          },
        },
      ],
      next_offset: null,
      generated_at_unix_ms: 1,
    });

    expect(page.cards[0]).toMatchObject({
      labelIDs: ["f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf"],
      preview: {
        markdown: "Bounded Markdown **preview**",
        truncated: true,
      },
      sourceWorkspace: { availability: "unlinked" },
    });
    expect(page.nextOffset).toBeNull();
  });

  it("decodes server-owned dependency progress and rejects invalid progress", () => {
    const response = {
      project_id: "project-1",
      workflow_id: "11111111-1111-4111-8111-111111111111",
      node_id: "node-1",
      cards: [
        {
          ...card,
          dependency_progress: { satisfied_count: 1, total_count: 2 },
        },
      ],
      next_offset: null,
      generated_at_unix_ms: 1,
    };

    expect(boardNodeCardsPageSchema.parse(response).cards[0]?.dependencyProgress).toEqual({
      satisfiedCount: 1,
      totalCount: 2,
    });
    expect(() =>
      boardNodeCardsPageSchema.parse({
        ...response,
        cards: [
          {
            ...card,
            dependency_progress: { satisfied_count: 1, total_count: 0 },
          },
        ],
      }),
    ).toThrow();
  });

  it("rejects legacy full bodies, flat previews, missing nested preview facts, and unknown workspace availability", () => {
    const legacyBodyCard = { ...card, body: "Complete Markdown **body**" };
    Reflect.deleteProperty(legacyBodyCard, "preview");
    expect(() =>
      boardNodeCardsPageSchema.parse({
        project_id: "project-1",
        workflow_id: "11111111-1111-4111-8111-111111111111",
        node_id: "node-1",
        cards: [legacyBodyCard],
        next_offset: null,
        generated_at_unix_ms: 1,
      }),
    ).toThrow();

    const flatPreviewCard = {
      ...card,
      preview_markdown: card.preview.markdown,
      preview_truncated: card.preview.truncated,
    };
    Reflect.deleteProperty(flatPreviewCard, "preview");
    expect(() =>
      boardNodeCardsPageSchema.parse({
        project_id: "project-1",
        workflow_id: "11111111-1111-4111-8111-111111111111",
        node_id: "node-1",
        cards: [flatPreviewCard],
        next_offset: null,
        generated_at_unix_ms: 1,
      }),
    ).toThrow();

    expect(() =>
      boardNodeCardsPageSchema.parse({
        project_id: "project-1",
        workflow_id: "11111111-1111-4111-8111-111111111111",
        node_id: "node-1",
        cards: [{ ...card, preview: { markdown: card.preview.markdown } }],
        next_offset: null,
        generated_at_unix_ms: 1,
      }),
    ).toThrow();

    expect(() =>
      boardNodeCardsPageSchema.parse({
        project_id: "project-1",
        workflow_id: "11111111-1111-4111-8111-111111111111",
        node_id: "node-1",
        cards: [
          {
            ...card,
            source_workspace: {
              ...workspace,
              availability: "mystery",
            },
          },
        ],
        next_offset: null,
        generated_at_unix_ms: 1,
      }),
    ).toThrow();
  });
});

describe("task activity schema", () => {
  const commentActivity = {
    activity_id: "activity-comment-1",
    type: "comment",
    task_id: "task-1",
    occurred_at_unix_ms: 1,
    updated_at_unix_ms: 1,
    comment: {
      id: "comment-1",
      task_id: "task-1",
      body: "Operator note",
      author: "user",
      created_at_unix_ms: 1,
      updated_at_unix_ms: 1,
    },
  };

  const sessionStartedActivity = {
    activity_id: "activity-session-1",
    type: "session_started",
    task_id: "task-1",
    occurred_at_unix_ms: 2,
    updated_at_unix_ms: 2,
    session_started: {
      session_id: "session-1",
      name: "Implementation",
    },
  };

  it("accepts only comment and session-started activity variants", () => {
    expect(
      activityPageSchema.parse({
        items: [commentActivity, sessionStartedActivity],
        next_page_token: "",
        generated_at_unix_ms: 2,
      }),
    ).toMatchObject({
      items: [
        { type: "comment", comment: { id: "comment-1" } },
        { type: "session_started", sessionID: "session-1", sessionName: "Implementation" },
      ],
    });
  });

  it("rejects removed persistence and history activity variants", () => {
    const legacyActivities = [
      { ...commentActivity, type: "run_interrupted", run: { id: "run-1" } },
      { ...commentActivity, type: "transition_applied", transition: { id: "transition-1" } },
      { ...commentActivity, type: "comment", actor: "GUI", summary: "Comment added" },
      { ...sessionStartedActivity, type: "session_started", history: { id: "history-1" } },
    ];

    for (const item of legacyActivities) {
      expect(() =>
        activityPageSchema.parse({
          items: [item],
          next_page_token: "",
          generated_at_unix_ms: 2,
        }),
      ).toThrow();
    }
  });
});
