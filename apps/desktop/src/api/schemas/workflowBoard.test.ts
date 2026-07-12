import { boardNodeCardsPageSchema, workflowBoardSchema } from "./workflowBoard";

const workspace = {
  workspace_id: "workspace-default",
  display_name: "Default workspace",
  root_path: "/workspace/default",
  availability: "available",
  is_primary: true,
  updated_at_unix_ms: 1,
};

const selectedWorkflow = {
  workflow_id: "workflow-1",
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
  body: "Complete Markdown **body**",
  workflow_id: "workflow-1",
  active_node_ids: [],
  source_workspace: workspace,
  status: {
    kind: "backlog",
    native_state: "active",
    node_ids: [],
    run_ids: [],
    attention_types: [],
  },
  actions: {
    can_start: true,
    can_interrupt: false,
    can_resume: false,
    can_cancel: true,
    manual_move_target_node_ids: [],
  },
  updated_at_unix_ms: 1,
};

describe("workflow board schemas", () => {
  it("decodes required parent workspace facts", () => {
    expect(workflowBoardSchema.parse(boardResponse)).toMatchObject({
      defaultWorkspaceID: "workspace-default",
      attachedWorkspaceCount: 2,
    });
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

  it("decodes a full card body and canonical detached workspace availability", () => {
    const page = boardNodeCardsPageSchema.parse({
      project_id: "project-1",
      workflow_id: "workflow-1",
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
      next_page_token: "",
      generated_at_unix_ms: 1,
    });

    expect(page.cards[0]).toMatchObject({
      body: "Complete Markdown **body**",
      sourceWorkspace: { availability: "unlinked" },
    });
  });

  it("rejects obsolete body previews and unknown workspace availability", () => {
    const cardWithoutBody = { ...card, body_preview: card.body };
    Reflect.deleteProperty(cardWithoutBody, "body");
    expect(() =>
      boardNodeCardsPageSchema.parse({
        project_id: "project-1",
        workflow_id: "workflow-1",
        node_id: "node-1",
        cards: [cardWithoutBody],
        next_page_token: "",
        generated_at_unix_ms: 1,
      }),
    ).toThrow();

    expect(() =>
      boardNodeCardsPageSchema.parse({
        project_id: "project-1",
        workflow_id: "workflow-1",
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
        next_page_token: "",
        generated_at_unix_ms: 1,
      }),
    ).toThrow();
  });
});
