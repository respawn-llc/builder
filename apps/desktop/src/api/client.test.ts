import { z } from "zod";

import { ApiClient } from "./client";
import { ContractError } from "./errors";
import { FakeRpcTransport } from "./fakeTransport";
import { protocolVersion } from "./jsonRpcSocket";

const startTaskParamsSchema = z.object({
  task_id: z.literal("task-1"),
  setup_operation_id: z.string(),
});

const appliedStartResponse = {
  outcome: "applied",
  applied: {
    transition_id: "transition-1",
    placement_id: "placement-1",
    run_id: "run-1",
  },
} as const;

describe("ApiClient", () => {
  it("parses readiness and sends mutation params through typed method boundary", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "server.readiness.get",
        result: {
          ready: true,
          server_id: "server-1",
          server_version: "1.3.0",
          protocol_version: protocolVersion,
          auth_ready: true,
          auth_required: false,
          endpoint: "ws://127.0.0.1:53082/rpc",
          subagent_roles: [{ name: "default" }, { name: "coder" }],
        },
      },
      { method: "workflow.task.start", result: appliedStartResponse },
    ]);
    const client = new ApiClient(transport);

    await expect(client.getReadiness()).resolves.toMatchObject({
      ready: true,
      serverID: "server-1",
      serverVersion: "1.3.0",
      protocolVersion: protocolVersion,
      subagentRoles: [{ name: "default" }, { name: "coder" }],
    });
    await expect(client.startTask("task-1")).resolves.toMatchObject({
      outcome: "applied",
      applied: {
        transitionID: "transition-1",
        placementID: "placement-1",
        runID: "run-1",
      },
    });

    const startCall = transport.calls.find((call) => call.method === "workflow.task.start");
    expect(startCall?.options).toEqual({ timeoutMs: null });
    expect(startTaskParamsSchema.parse(startCall?.params).task_id).toBe("task-1");
  });

  it("rejects server contract drift before feature code receives raw data", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([{ method: "server.readiness.get", result: { ready: true } }]),
    );

    await expect(client.getReadiness()).rejects.toBeInstanceOf(ContractError);
  });

  it("normalizes empty workflow board metadata and node-card slices returned as null by Go JSON", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([
        { method: "workflow.board.get", result: emptyBoardResponse },
        { method: "workflow.board.nodeCards.list", result: emptyBoardNodeCardsResponse },
      ]),
    );

    await expect(client.getBoard("project-1", "")).resolves.toMatchObject({
      projectID: "project-1",
      workflows: [],
      groups: [],
      columns: [],
    });
    await expect(
      client.listBoardNodeCards("project-1", "workflow-1", "node-1", "cursor-1"),
    ).resolves.toMatchObject({
      projectID: "project-1",
      workflowID: "workflow-1",
      nodeID: "node-1",
      cards: [],
      nextPageToken: "cursor-2",
    });
  });

  it("hides workflow join nodes from board columns and groups", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([{ method: "workflow.board.get", result: boardWithJoinResponse }]),
    );

    await expect(client.getBoard("project-1", "workflow-1")).resolves.toMatchObject({
      groups: [{ id: "group-1", nodeIDs: ["node-agent"] }],
      columns: [{ id: "node-agent", kind: "agent" }],
    });
  });

  it("normalizes empty task detail slices returned as null by Go JSON", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([{ method: "workflow.task.get", result: emptyTaskDetailResponse }]),
    );

    await expect(client.getTask("task-1")).resolves.toMatchObject({
      id: "task-1",
      runs: [],
      transitions: [],
      comments: [],
      attention: [],
      sourceURL: "",
    });
  });

  it("parses task source URL into sourceURL", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([
        {
          method: "workflow.task.get",
          result: {
            task: {
              ...emptyTaskDetailResponse.task,
              source_url: "https://github.com/respawn-llc/kent/issues/1",
            },
          },
        },
      ]),
    );

    await expect(client.getTask("task-1")).resolves.toMatchObject({
      sourceURL: "https://github.com/respawn-llc/kent/issues/1",
    });
  });

  it("uses paginated task comment list RPC contract", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.task.comment.list",
        result: {
          comments: [commentResponse],
          next_page_token: "cursor-2",
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.listTaskComments("task-1", "cursor-1")).resolves.toMatchObject({
      comments: [{ id: "comment-1", body: "Existing comment" }],
      nextPageToken: "cursor-2",
    });
    expect(transport.calls).toContainEqual({
      method: "workflow.task.comment.list",
      params: { task_id: "task-1", page_size: 40, page_token: "cursor-1" },
    });
  });

  it("uses project edit workspace pagination and mutation RPC contracts", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "project.edit.get",
        result: {
          project_id: "project-1",
          project_key: "PROJ",
          display_name: "Project",
          default_workspace_id: "workspace-1",
          workspaces: [workspaceResponse],
          next_page_token: "cursor-2",
        },
      },
      { method: "project.update", result: { project: projectSummaryResponse } },
      { method: "project.defaultWorkspace.set", result: { project: projectSummaryResponse } },
      {
        method: "project.unlinkWorkspace",
        result: {
          project_id: "project-1",
          workspace_id: "workspace-1",
          unlinked: false,
          blockers: [{ code: "default_workspace", message: "Default workspace cannot be unlinked." }],
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.getProjectEdit("project-1", "cursor-1")).resolves.toMatchObject({
      projectID: "project-1",
      nextPageToken: "cursor-2",
    });
    await client.updateProject("project-1", "Renamed");
    await client.updateProject("project-1", "Renamed", "ABC");
    await client.setDefaultWorkspace("project-1", "workspace-1");
    await expect(client.unlinkWorkspace("project-1", "workspace-1")).resolves.toMatchObject({
      unlinked: false,
      blockers: [{ code: "default_workspace", count: 0 }],
    });

    expect(transport.calls).toContainEqual({
      method: "project.edit.get",
      params: { project_id: "project-1", page_size: 100, page_token: "cursor-1" },
    });
    expect(transport.calls).toContainEqual({
      method: "project.update",
      params: { project_id: "project-1", display_name: "Renamed", project_key: "" },
    });
    expect(transport.calls).toContainEqual({
      method: "project.update",
      params: { project_id: "project-1", display_name: "Renamed", project_key: "ABC" },
    });
    expect(transport.calls).toContainEqual({
      method: "project.defaultWorkspace.set",
      params: { project_id: "project-1", workspace_id: "workspace-1" },
    });
    expect(transport.calls).toContainEqual({
      method: "project.unlinkWorkspace",
      params: { project_id: "project-1", workspace_id: "workspace-1" },
    });
  });

  it("maps workflow definition, execution validation, and active project links for the editor", async () => {
    const transport = new FakeRpcTransport([
      { method: "workflow.get", result: workflowDefinitionResponse },
      { method: "workflow.validate", result: workflowValidationResponse },
      { method: "workflow.listProjectLinks", result: workflowLinksResponse },
    ]);
    const client = new ApiClient(transport);

    const definition = await client.getWorkflow("workflow-1");
    expect(definition).toMatchObject({
      derivedWiring: {
        edges: [
          {
            edgeID: "edge-1",
            inputBindings: [{ field: "summary", name: "summary", source: "transition_output" }],
            requiredProvisionFields: [{ description: "Summary", name: "summary" }],
          },
        ],
      },
      workflow: { id: "workflow-1", name: "Delivery", version: 9 },
      nodeGroups: [{ id: "group-1", key: "core", name: "Core", nodeIDs: [] }],
      transitionGroups: [
        {
          description: "Choose this when implementation is complete.",
          id: "tg-1",
          sourceNodeID: "node-1",
          transitionID: "done",
        },
      ],
      edges: [
        {
          contextSource: { kind: "selected_node", nodeKey: "implement" },
          id: "edge-1",
          parameters: [{ description: "Summary", key: "summary" }],
          promptTemplate: "Summarize the implementation.",
          targetNodeID: "done",
          transitionGroupID: "tg-1",
        },
      ],
    });
    expect(definition.nodes).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ id: "node-1", name: "Implement", subagentRole: "coder" }),
      ]),
    );
    await expect(client.validateWorkflow("workflow-1", "execution")).resolves.toMatchObject({
      valid: false,
      errors: [
        {
          code: "workflow.validation.invalid",
          workflowID: "workflow-1",
          nodeID: "node-1",
          transitionGroupID: "tg-1",
          edgeID: "edge-1",
          details: {
            fieldName: "",
            inputName: "summary",
            placeholder: ".Params.summary",
            providerEdgeID: "",
          },
          relatedIDs: ["edge-2"],
          blocksContext: true,
        },
      ],
    });
    await expect(client.listProjectWorkflowLinks("project-1")).resolves.toEqual([
      {
        id: "link-1",
        projectID: "project-1",
        workflowID: "workflow-1",
        isDefault: true,
      },
    ]);

    expect(transport.calls).toContainEqual({
      method: "workflow.get",
      params: { workflow_id: "workflow-1" },
    });
    expect(transport.calls).toContainEqual({
      method: "workflow.validate",
      params: { workflow_id: "workflow-1", mode: "execution" },
    });
    expect(transport.calls).toContainEqual({
      method: "workflow.listProjectLinks",
      params: { project_id: "project-1" },
    });
  });

  it("maps previous-target-or-new workflow context sources", async () => {
    const response = {
      definition: {
        ...workflowDefinitionResponse.definition,
        edges: workflowDefinitionResponse.definition.edges.map((edge) => ({
          ...edge,
          context_source: { kind: "previous_target_or_new", node_key: "" },
        })),
      },
    };
    const transport = new FakeRpcTransport([{ method: "workflow.get", result: response }]);
    const client = new ApiClient(transport);

    await expect(client.getWorkflow("workflow-1")).resolves.toMatchObject({
      edges: [
        {
          contextSource: { kind: "previous_target_or_new", nodeKey: "" },
          id: "edge-1",
        },
      ],
    });
  });

  it("maps workflow library list, create, link, and project create-link contracts", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.list",
        result: {
          workflows: [
            {
              id: "workflow-1",
              name: "Delivery",
              description: "Ship",
              version: 4,
              execution_target_policy: { mode: "custom_ref", custom_ref: "release/v1" },
            },
          ],
          next_page_token: "cursor-2",
        },
      },
      {
        method: "workflow.create",
        result: {
          workflow: {
            id: "workflow-2",
            name: "Ops",
            description: "",
            version: 1,
            execution_target_policy: { mode: "ask_on_first_execution" },
          },
        },
      },
      {
        method: "workflow.createAndLinkProject",
        result: {
          workflow: {
            id: "workflow-3",
            name: "Project workflow",
            description: "",
            version: 1,
            execution_target_policy: { mode: "none" },
          },
          link: { id: "link-3", project_id: "project-1", workflow_id: "workflow-3", default: true },
        },
      },
      {
        method: "workflow.linkProject",
        result: {
          link: { id: "link-1", project_id: "project-1", workflow_id: "workflow-1", default: false },
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(
      client.listWorkflows({ pageSize: 10, pageToken: "cursor-1", query: "ship" }),
    ).resolves.toMatchObject({
      nextPageToken: "cursor-2",
      workflows: [
        {
          id: "workflow-1",
          name: "Delivery",
          version: 4,
          executionTargetPolicy: { mode: "custom_ref", customRef: "release/v1" },
        },
      ],
    });
    await expect(client.createWorkflow({ name: "Ops", description: "" })).resolves.toMatchObject({
      id: "workflow-2",
      name: "Ops",
      executionTargetPolicy: { mode: "ask_on_first_execution", customRef: null },
    });
    await expect(
      client.createAndLinkWorkflowToProject({
        projectID: "project-1",
        name: "Project workflow",
        description: "",
      }),
    ).resolves.toMatchObject({
      link: { isDefault: true, projectID: "project-1", workflowID: "workflow-3" },
      workflow: { id: "workflow-3" },
    });
    await expect(
      client.linkWorkflowToProject({ projectID: "project-1", workflowID: "workflow-1" }),
    ).resolves.toMatchObject({
      id: "link-1",
      isDefault: false,
    });

    expect(transport.calls).toContainEqual({
      method: "workflow.list",
      params: { page_size: 10, page_token: "cursor-1", query: "ship" },
    });
    expect(transport.calls).toContainEqual({
      method: "workflow.createAndLinkProject",
      params: {
        name: "Project workflow",
        description: "",
        project_id: "project-1",
        default_policy: "if_project_has_none",
      },
    });
    expect(transport.calls).toContainEqual({
      method: "workflow.linkProject",
      params: {
        project_id: "project-1",
        workflow_id: "workflow-1",
        default_policy: "if_project_has_none",
      },
    });
  });

  it("maps workflow delete preview and confirmed delete contracts", async () => {
    const transport = new FakeRpcTransport([
      { method: "workflow.deletePreview", result: workflowDeletePreviewResponse },
      { method: "workflow.delete", result: workflowDeleteResponse },
    ]);
    const client = new ApiClient(transport);

    await expect(client.previewWorkflowDelete("workflow-1")).resolves.toMatchObject({
      workflowID: "workflow-1",
      version: 7,
      projectCount: 1,
      linkCount: 1,
      defaultReplacementProjectCount: 0,
      taskCount: 2,
      activeRunCount: 0,
      runnableRunCount: 1,
      blockedTaskCount: 1,
    });
    await expect(
      client.deleteWorkflow({
        workflowID: "workflow-1",
        confirmed: true,
        expectedVersion: 7,
        expectedProjectCount: 1,
        expectedLinkCount: 1,
        expectedTaskCount: 2,
        cleanupArtifacts: false,
      }),
    ).resolves.toMatchObject({
      deleted: false,
      blockers: [{ code: "runnable_runs", count: 1 }],
    });

    expect(transport.calls).toContainEqual({
      method: "workflow.deletePreview",
      params: { workflow_id: "workflow-1" },
    });
    expect(transport.calls).toContainEqual({
      method: "workflow.delete",
      params: {
        workflow_id: "workflow-1",
        confirmed: true,
        expected_version: 7,
        expected_project_count: 1,
        expected_link_count: 1,
        expected_task_count: 2,
        cleanup_artifacts: false,
      },
    });
  });

  it("maps workflow graph draft validation, preview, and save contracts", async () => {
    const graphValidationResults = {
      draft: { valid: true, errors: [] },
      execution: workflowValidationResponse,
    };
    const transport = new FakeRpcTransport([
      {
        method: "workflow.graph.validateDraft",
        result: {
          results: graphValidationResults,
          derived_wiring: {
            edges: [
              {
                edge_id: "edge-start",
                input_bindings: [{ name: "brief", source: "transition_output", field: "brief" }],
                required_provision_fields: [{ name: "brief", description: "Brief" }],
              },
            ],
          },
        },
      },
      {
        method: "workflow.graph.savePreview",
        result: {
          current_version: 11,
          validation_results: graphValidationResults,
          impact: workflowGraphSaveImpactResponse,
          blockers: [{ code: "confirmation_required", message: "Confirm removal.", count: 1 }],
          can_save: false,
          confirmation_required: true,
        },
      },
      {
        method: "workflow.graph.save",
        result: {
          saved: true,
          definition: workflowDefinitionResponse.definition,
          current_version: 12,
          validation_results: graphValidationResults,
          impact: { ...workflowGraphSaveImpactResponse, removed_edge_count: 0 },
          blockers: null,
          can_save: true,
          confirmation_required: false,
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(
      client.validateWorkflowGraphDraft({
        workflowID: "workflow-1",
        metadata: {
          name: "Draft Workflow",
          description: "Draft description",
          executionTargetPolicy: { mode: "custom_ref", customRef: "release/v1" },
        },
        graph: workflowGraphDraft,
        modes: ["draft", "execution"],
      }),
    ).resolves.toMatchObject({
      draft: { valid: true },
      execution: { valid: false },
      derivedWiring: {
        edges: [
          {
            edgeID: "edge-start",
            inputBindings: [{ field: "brief", name: "brief", source: "transition_output" }],
            requiredProvisionFields: [{ description: "Brief", name: "brief" }],
          },
        ],
      },
    });
    await expect(
      client.previewWorkflowGraphSave({
        workflowID: "workflow-1",
        expectedVersion: 11,
        metadata: {
          name: "Preview Workflow",
          description: "Preview description",
          executionTargetPolicy: { mode: "default_branch", customRef: null },
        },
        graph: workflowGraphDraft,
      }),
    ).resolves.toMatchObject({
      currentVersion: 11,
      confirmationRequired: true,
      impact: { removedEdgeCount: 1 },
      blockers: [{ code: "confirmation_required" }],
    });
    await expect(
      client.saveWorkflowGraph({
        workflowID: "workflow-1",
        expectedVersion: 11,
        metadata: {
          name: "Saved Workflow",
          description: "Saved description",
          executionTargetPolicy: { mode: "none", customRef: null },
        },
        graph: workflowGraphDraft,
        confirmation: {
          expectedRemovedNodeCount: 0,
          expectedRemovedTransitionGroupCount: 0,
          expectedRemovedEdgeCount: 1,
          expectedNodeTaskReferenceCount: 0,
          expectedEdgeTaskReferenceCount: 0,
        },
      }),
    ).resolves.toMatchObject({
      saved: true,
      currentVersion: 12,
      definition: { workflow: { id: "workflow-1" } },
      blockers: [],
    });

    expect(transport.calls[0]).toEqual({
      method: "workflow.graph.validateDraft",
      params: {
        workflow_id: "workflow-1",
        metadata: {
          name: "Draft Workflow",
          description: "Draft description",
          execution_target_policy: { mode: "custom_ref", custom_ref: "release/v1" },
        },
        modes: ["draft", "execution"],
        graph: {
          node_groups: [],
          nodes: [
            {
              id: "node-start",
              key: "backlog",
              kind: "start",
              display_name: "Backlog",
              input_fields: [],
              join_input_providers: [],
            },
          ],
          transition_groups: [
            {
              id: "group-start",
              source_node_id: "node-start",
              transition_id: "start",
              display_name: "Start",
              description: "Start the workflow.",
            },
          ],
          edges: [
            {
              id: "edge-start",
              transition_group_id: "group-start",
              key: "start",
              target_node_id: "node-agent",
              requires_approval: false,
              context_mode: "new_session",
              context_source: { kind: "immediate_source", node_key: "" },
              parameters: [{ description: "Brief", key: "brief" }],
              prompt_template: "Start from {{.TaskTitle}}.",
            },
          ],
        },
      },
    });
    expect(transport.calls[2]).toMatchObject({
      method: "workflow.graph.save",
      params: {
        expected_version: 11,
        metadata: {
          name: "Saved Workflow",
          description: "Saved description",
          execution_target_policy: { mode: "none" },
        },
        confirmation: {
          expected_removed_edge_count: 1,
        },
      },
    });
  });
});

const emptyWorkflow = {
  workflow_id: "",
  display_name: "",
  description: "",
  version: 0,
  is_project_default: false,
  valid_for_task_creation: false,
  validation_errors: null,
};

const emptyBoardResponse = {
  board: {
    project_id: "project-1",
    project: { project_key: "proj", display_name: "Project" },
    selected_workflow: emptyWorkflow,
    workflows: null,
    groups: null,
    columns: null,
    generated_at_unix_ms: 1,
  },
};

const boardWithJoinResponse = {
  board: {
    ...emptyBoardResponse.board,
    groups: [
      {
        group_id: "group-1",
        key: "review",
        display_name: "Review",
        sort_order: 1,
        node_ids: ["node-agent", "node-join"],
      },
      {
        group_id: "group-join-only",
        key: "join_only",
        display_name: "Join Only",
        sort_order: 2,
        node_ids: ["node-join"],
      },
    ],
    columns: [boardColumnResponse("node-agent", "agent"), boardColumnResponse("node-join", "join")],
  },
};

function boardColumnResponse(nodeID: string, kind: string) {
  return {
    node: {
      node_id: nodeID,
      key: nodeID,
      kind,
      display_name: nodeID,
      assignee_role: "",
      output_fields: [],
      transition_output_fields: [],
    },
    group_id: "group-1",
    sort_order: 1,
    is_backlog: false,
    is_done: false,
    task_count: 0,
  };
}

const emptyBoardNodeCardsResponse = {
  project_id: "project-1",
  workflow_id: "workflow-1",
  node_id: "node-1",
  cards: null,
  next_page_token: "cursor-2",
  generated_at_unix_ms: 1,
};

const workspaceResponse = {
  workspace_id: "workspace-1",
  display_name: "Project",
  root_path: "/tmp/project",
  availability: "available",
  is_primary: true,
  updated_at_unix_ms: 1,
};

const projectSummaryResponse = {
  project_id: "project-1",
  project_key: "PROJ",
  display_name: "Project",
  primary_workspace: workspaceResponse,
  default_workflow_id: "workflow-1",
  default_workflow_name: "Delivery",
  default_workflow_valid: true,
  updated_at_unix_ms: 1,
  task_count: 0,
  attention_count: 0,
  workflow_count: 1,
};

const commentResponse = {
  id: "comment-1",
  task_id: "task-1",
  body: "Existing comment",
  author: "user",
  created_at_unix_ms: 1,
  updated_at_unix_ms: 1,
};

const emptyTaskDetailResponse = {
  task: {
    summary: {
      id: "task-1",
      project_id: "project-1",
      workflow_id: "workflow-1",
      short_id: "PROJ-1",
      title: "Task",
      created_at_unix_ms: 1,
      updated_at_unix_ms: 1,
      done: false,
      canceled_at_unix_ms: null,
    },
    project: {
      display_name: "Project",
    },
    workflow: {
      workflow_id: "workflow-1",
      display_name: "Delivery",
      description: "",
      version: 1,
      is_project_default: true,
      valid_for_task_creation: true,
      validation_errors: null,
    },
    body: "Body",
    source_workspace: workspaceResponse,
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
    attention: null,
    runs: null,
    transitions: null,
    comments: null,
  },
};

const workflowDefinitionResponse = {
  definition: {
    workflow: {
      id: "workflow-1",
      name: "Delivery",
      description: "Delivery workflow",
      version: 9,
      execution_target_policy: { mode: "head" },
    },
    node_groups: [
      {
        group_id: "group-1",
        workflow_id: "workflow-1",
        group_key: "core",
        display_name: "Core",
        sort_order: 1,
      },
    ],
    nodes: [
      {
        id: "node-1",
        workflow_id: "workflow-1",
        key: "implement",
        kind: "agent",
        display_name: "Implement",
        group_id: "group-1",
        group_key: "core",
        subagent_role: "coder",
        output_fields: null,
      },
      {
        id: "done",
        workflow_id: "workflow-1",
        key: "done",
        kind: "terminal",
        display_name: "Done",
      },
    ],
    transition_groups: [
      {
        id: "tg-1",
        workflow_id: "workflow-1",
        source_node_id: "node-1",
        transition_id: "done",
        display_name: "Done",
        description: "Choose this when implementation is complete.",
      },
    ],
    edges: [
      {
        id: "edge-1",
        workflow_id: "workflow-1",
        transition_group_id: "tg-1",
        key: "done",
        target_node_id: "done",
        requires_approval: false,
        context_mode: "new_session",
        context_source: {
          kind: "selected_node",
          node_key: "implement",
        },
        prompt_template: "Summarize the implementation.",
        parameters: [{ key: "summary", description: "Summary" }],
        input_bindings: null,
        output_requirements: null,
      },
    ],
    derived_wiring: {
      nodes: [
        {
          node_id: "node-1",
          possible_provision_fields: [{ name: "summary", description: "Summary" }],
        },
      ],
      transition_groups: [
        {
          transition_group_id: "tg-1",
          required_provision_fields: [{ name: "summary", description: "Summary" }],
        },
      ],
      edges: [
        {
          edge_id: "edge-1",
          input_bindings: [{ name: "summary", source: "transition_output", field: "summary" }],
          required_provision_fields: [{ name: "summary", description: "Summary" }],
        },
      ],
    },
  },
};

const workflowValidationResponse = {
  valid: false,
  errors: [
    {
      code: "workflow.validation.invalid",
      message: "Invalid edge",
      workflow_id: "workflow-1",
      node_id: "node-1",
      transition_group_id: "tg-1",
      edge_id: "edge-1",
      details: {
        input_name: "summary",
        placeholder: ".Params.summary",
      },
      related_ids: ["edge-2"],
      blocks_context: true,
    },
  ],
};

const workflowLinksResponse = {
  links: [
    {
      id: "link-1",
      project_id: "project-1",
      workflow_id: "workflow-1",
      default: true,
    },
  ],
};

const workflowDeleteImpactResponse = {
  workflow_id: "workflow-1",
  version: 7,
  project_count: 1,
  link_count: 1,
  default_replacement_project_count: 0,
  task_count: 2,
  active_run_count: 0,
  runnable_run_count: 1,
  blocked_task_count: 1,
};

const workflowDeletePreviewResponse = {
  impact: workflowDeleteImpactResponse,
};

const workflowDeleteResponse = {
  deleted: false,
  impact: workflowDeleteImpactResponse,
  blockers: [{ code: "runnable_runs", message: "Workflow has runnable runs.", count: 1 }],
};

const workflowGraphSaveImpactResponse = {
  removed_node_count: 0,
  removed_transition_group_count: 0,
  removed_edge_count: 1,
  node_task_reference_count: 0,
  edge_task_reference_count: 0,
  active_node_placement_count: 0,
  pending_approval_count: 0,
  active_run_count: 0,
  runnable_run_count: 0,
  start_node_change_count: 0,
  last_terminal_change_count: 0,
  task_referenced_node_kind_change_count: 0,
};

const workflowGraphDraft = {
  nodeGroups: [],
  nodes: [
    {
      id: "node-start",
      key: "backlog",
      kind: "start",
      name: "Backlog",
      groupID: "",
      groupKey: "",
      subagentRole: "",
      promptTemplate: "",
      inputFields: [],
      joinInputProviders: [],
    },
  ],
  transitionGroups: [
    {
      id: "group-start",
      sourceNodeID: "node-start",
      transitionID: "start",
      name: "Start",
      description: "Start the workflow.",
    },
  ],
  edges: [
    {
      id: "edge-start",
      transitionGroupID: "group-start",
      key: "start",
      targetNodeID: "node-agent",
      requiresApproval: false,
      contextMode: "new_session",
      contextSource: { kind: "immediate_source", nodeKey: "" },
      promptTemplate: "Start from {{.TaskTitle}}.",
      parameters: [{ key: "brief", description: "Brief" }],
    },
  ],
};
