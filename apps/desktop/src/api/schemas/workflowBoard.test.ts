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
  preview: {
    markdown: "Bounded Markdown **preview**",
    truncated: true,
  },
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
      previous_page_token: null,
      next_page_token: null,
      generated_at_unix_ms: 1,
    });

    expect(page.cards[0]).toMatchObject({
      preview: {
        markdown: "Bounded Markdown **preview**",
        truncated: true,
      },
      sourceWorkspace: { availability: "unlinked" },
    });
    expect(page.previousPageToken).toBeNull();
    expect(page.nextPageToken).toBeNull();
  });

  it("rejects legacy full bodies, flat previews, missing nested preview facts, and unknown workspace availability", () => {
    const legacyBodyCard = { ...card, body: "Complete Markdown **body**" };
    Reflect.deleteProperty(legacyBodyCard, "preview");
    expect(() =>
      boardNodeCardsPageSchema.parse({
        project_id: "project-1",
        workflow_id: "workflow-1",
        node_id: "node-1",
        cards: [legacyBodyCard],
        previous_page_token: null,
        next_page_token: null,
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
        workflow_id: "workflow-1",
        node_id: "node-1",
        cards: [flatPreviewCard],
        previous_page_token: null,
        next_page_token: null,
        generated_at_unix_ms: 1,
      }),
    ).toThrow();

    expect(() =>
      boardNodeCardsPageSchema.parse({
        project_id: "project-1",
        workflow_id: "workflow-1",
        node_id: "node-1",
        cards: [{ ...card, preview: { markdown: card.preview.markdown } }],
        previous_page_token: null,
        next_page_token: null,
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
        previous_page_token: null,
        next_page_token: null,
        generated_at_unix_ms: 1,
      }),
    ).toThrow();
  });
});
