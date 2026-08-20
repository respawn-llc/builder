import { z } from "zod";
import { create } from "@app/server-api-contract";
import { ServerService } from "@app/server-api-contract/gen/kent/api/server/server_pb";

import { ApiClient } from "./client";
import { FakeRpcTransport } from "@/test-support/api";
import { protocolVersion } from "./jsonRpcSocket";
import { canonicalBoardFilter } from "./workflowBoardFilters";
import {
  workflowBoundaryGraphIDs as boundaryGraphIDs,
  workflowGraphDraft,
  workflowGraphDraftIDs,
} from "./clientWorkflowGraph.testFixtures";

const startTaskParamsSchema = z.object({
  task_id: z.literal("task-1"),
  setup_operation_id: z.string(),
});

const appliedStartResponse = {
  outcome: "applied",
  applied: {
    current_nodes: [{ node_id: "node-1", transition_branch_key: null, session_id: null }],
  },
} as const;

describe("ApiClient", () => {
  it("parses readiness and sends mutation params through typed method boundary", async () => {
    const transport = new FakeRpcTransport([
      {
        descriptor: ServerService.method.getReadiness,
        result: create(ServerService.method.getReadiness.output, {
          outcome: {
            case: "success",
            value: {
              readiness: {
                ready: true,
                serverId: "server-1",
                serverVersion: "1.3.0",
                serverBuild: "1.3.0",
                protocolVersion,
                authReady: true,
                endpoint: "ws://127.0.0.1:53082/rpc",
                subagentRoles: [{ name: "default" }, { name: "coder" }],
              },
            },
          },
        }),
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
    expect(transport.descriptorCalls[0]?.descriptor).toBe(ServerService.method.getReadiness);
    await expect(client.startTask({ taskID: "task-1" })).resolves.toMatchObject({
      outcome: "applied",
      applied: {
        currentNodes: [{ nodeID: "node-1", transitionBranchKey: null, sessionID: null }],
      },
    });

    const startCall = transport.calls.find((call) => call.method === "workflow.task.start");
    expect(startCall?.options).toEqual({ timeoutMs: null });
    expect(startTaskParamsSchema.parse(startCall?.params).task_id).toBe("task-1");
  });

  it("preserves absent board workflow selectors and normalizes empty slices", async () => {
    const transport = new FakeRpcTransport([
      { method: "workflow.board.get", result: emptyBoardResponse },
      { method: "workflow.board.nodeCards.list", result: emptyBoardNodeCardsResponse },
      { method: "workflow.board.get", result: emptyBoardResponse },
      { method: "workflow.board.nodeCards.list", result: emptyBoardNodeCardsResponse },
    ]);
    const client = new ApiClient(transport);

    await expect(
      client.getBoard(
        "project-1",
        undefined,
        canonicalBoardFilter({ labelFilter: { kind: "none" }, dependencyFilter: null }),
      ),
    ).resolves.toMatchObject({
      projectID: "project-1",
      selectedWorkflow: null,
      workflows: [],
      groups: [],
      columns: [],
    });
    expect(transport.calls).toEqual([
      {
        method: "workflow.board.get",
        params: { project_id: "project-1", label_filter: { kind: "none" }, dependency_filter: null },
      },
    ]);
    const labelID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
    await expect(
      client.listBoardNodeCards({
        projectID: "project-1",
        workflowID: "11111111-1111-4111-8111-111111111111",
        nodeID: "node-1",
        filter: canonicalBoardFilter({
          labelFilter: { kind: "named", mode: "all", labelIDs: [labelID] },
          dependencyFilter: null,
        }),
        offset: 25,
        sort: { field: "labels", direction: "asc" },
      }),
    ).resolves.toMatchObject({
      projectID: "project-1",
      workflowID: "11111111-1111-4111-8111-111111111111",
      nodeID: "node-1",
      cards: [],
      nextOffset: 50,
    });
    expect(transport.calls).toContainEqual({
      method: "workflow.board.nodeCards.list",
      params: {
        project_id: "project-1",
        workflow_id: "11111111-1111-4111-8111-111111111111",
        node_id: "node-1",
        label_filter: { kind: "named", named: { mode: "all", label_ids: [labelID] } },
        dependency_filter: null,
        page_size: 25,
        sort: { field: "labels", direction: "asc" },
        offset: 25,
      },
    });
    const unblockedFilter = canonicalBoardFilter({ labelFilter: { kind: "none" }, dependencyFilter: true });
    await Promise.all([
      client.getBoard("project-1", "11111111-1111-4111-8111-111111111111", unblockedFilter),
      client.listBoardNodeCards({
        projectID: "project-1",
        workflowID: "11111111-1111-4111-8111-111111111111",
        nodeID: "node-1",
        filter: unblockedFilter,
        offset: 0,
        sort: { field: "updated", direction: "desc" },
      }),
    ]);
    expect(transport.calls.slice(2).map(({ method }) => method)).toEqual([
      "workflow.board.get",
      "workflow.board.nodeCards.list",
    ]);
    expect(transport.calls[2]?.params).toMatchObject({ dependency_filter: true });
    expect(transport.calls[3]?.params).toMatchObject({ dependency_filter: true });
  });

  it("rejects malformed Workflow IDs before direct client RPCs or subscriptions", async () => {
    const transport = new FakeRpcTransport([]);
    const client = new ApiClient(transport);
    const prefixedID = "workflow-11111111-1111-4111-8111-111111111111";

    await expect(client.getWorkflow(prefixedID)).rejects.toThrow();
    await expect(client.previewWorkflowDelete("not-a-workflow-id")).rejects.toThrow();
    expect(() =>
      client.subscribeWorkflow(prefixedID, {
        onEvent: () => undefined,
        onComplete: () => undefined,
        onError: () => undefined,
      }),
    ).toThrow();

    expect(transport.calls).toEqual([]);
    expect(transport.subscriptions).toEqual([]);
  });

  it("hides workflow join nodes from board columns and groups", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([{ method: "workflow.board.get", result: boardWithJoinResponse }]),
    );

    await expect(
      client.getBoard(
        "project-1",
        "11111111-1111-4111-8111-111111111111",
        canonicalBoardFilter({ labelFilter: { kind: "none" }, dependencyFilter: null }),
      ),
    ).resolves.toMatchObject({
      groups: [{ id: boundaryGraphIDs.nodeGroup, nodeIDs: [boundaryGraphIDs.node] }],
      columns: [{ id: boundaryGraphIDs.node, kind: "agent" }],
    });
  });

  it("parses required empty current task execution arrays", async () => {
    const client = new ApiClient(
      new FakeRpcTransport([{ method: "workflow.task.get", result: emptyTaskDetailResponse }]),
    );

    await expect(client.getTask("task-1")).resolves.toMatchObject({
      id: "task-1",
      labelIDs: ["f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf"],
      currentNodes: [],
      liveSessions: [],
      currentScripts: [],
      attentionCount: 0,
      sourceURL: "",
    });
  });

  it("uses separate global and task attention RPC contracts", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.attention.list",
        result: { items: [], next_page_token: "", generated_at_unix_ms: 1 },
      },
      {
        method: "workflow.task.attention.list",
        result: { items: [], generated_at_unix_ms: 2 },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(client.listAttention("cursor-1")).resolves.toMatchObject({ items: [], nextPageToken: "" });
    await expect(client.listTaskAttention("task-1")).resolves.toMatchObject({ items: [], generatedAt: 2 });

    expect(transport.calls).toEqual([
      {
        method: "workflow.attention.list",
        params: { page_size: 40, page_token: "cursor-1" },
      },
      {
        method: "workflow.task.attention.list",
        params: { task_id: "task-1" },
      },
    ]);
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

  it("maps workflow definition, execution validation, and active project links for the editor", async () => {
    const transport = new FakeRpcTransport([
      { method: "workflow.get", result: workflowDefinitionResponse },
      { method: "workflow.validate", result: workflowValidationResponse },
      { method: "workflow.listProjectLinks", result: workflowLinksResponse },
    ]);
    const client = new ApiClient(transport);

    const definition = await client.getWorkflow("11111111-1111-4111-8111-111111111111");
    expect(definition).toMatchObject({
      derivedWiring: {
        edges: [
          {
            edgeID: boundaryGraphIDs.edge,
            inputBindings: [{ field: "summary", name: "summary", source: "transition_output" }],
            requiredProvisionFields: [{ description: "Summary", name: "summary" }],
            assigneeSelectionApplicability: { available: true, reason: "eligible" },
            thinkingSelectionApplicability: { available: true, reason: "eligible" },
          },
        ],
      },
      workflow: { id: "11111111-1111-4111-8111-111111111111", name: "Delivery", version: 9 },
      nodeGroups: [{ id: boundaryGraphIDs.nodeGroup, key: "core", name: "Core", nodeIDs: [] }],
      transitionGroups: [
        {
          description: "Choose this when implementation is complete.",
          id: boundaryGraphIDs.transitionGroup,
          sourceNodeID: boundaryGraphIDs.node,
          transitionID: "done",
        },
      ],
      edges: [
        {
          contextSource: { kind: "selected_node", nodeKey: "implement" },
          id: boundaryGraphIDs.edge,
          assigneeSelection: "configured",
          thinkingSelection: "configured",
          parameters: [{ description: "Summary", key: "summary", purpose: "ordinary" }],
          promptTemplate: "Summarize the implementation.",
          targetNodeID: boundaryGraphIDs.doneNode,
          transitionGroupID: boundaryGraphIDs.transitionGroup,
        },
      ],
    });
    expect(definition.nodes).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          groupID: boundaryGraphIDs.nodeGroup,
          id: boundaryGraphIDs.node,
          name: "Implement",
          subagentRole: "coder",
        }),
        expect.objectContaining({ groupID: null, id: boundaryGraphIDs.doneNode, name: "Done" }),
      ]),
    );
    await expect(
      client.validateWorkflow("11111111-1111-4111-8111-111111111111", "execution"),
    ).resolves.toMatchObject({
      valid: false,
      errors: [
        {
          code: "workflow.validation.invalid",
          workflowID: "11111111-1111-4111-8111-111111111111",
          nodeID: boundaryGraphIDs.node,
          transitionGroupID: boundaryGraphIDs.transitionGroup,
          edgeID: boundaryGraphIDs.edge,
          details: {
            fieldName: "",
            inputName: "summary",
            placeholder: ".Params.summary",
            providerEdgeID: null,
          },
          relatedIDs: [boundaryGraphIDs.relatedEdge],
          blocksContext: true,
        },
      ],
    });
    await expect(client.listProjectWorkflowLinks("project-1")).resolves.toEqual([
      {
        id: "link-1",
        projectID: "project-1",
        workflowID: "11111111-1111-4111-8111-111111111111",
        isDefault: true,
      },
    ]);

    expect(transport.calls).toContainEqual({
      method: "workflow.get",
      params: { workflow_id: "11111111-1111-4111-8111-111111111111" },
    });
    expect(transport.calls).toContainEqual({
      method: "workflow.validate",
      params: { workflow_id: "11111111-1111-4111-8111-111111111111", mode: "execution" },
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

    await expect(client.getWorkflow("11111111-1111-4111-8111-111111111111")).resolves.toMatchObject({
      edges: [
        {
          contextSource: { kind: "previous_target_or_new", nodeKey: "" },
          id: boundaryGraphIDs.edge,
        },
      ],
    });
  });

  it("maps workflow library list, create, link, and project create-link contracts", async () => {
    const transport = new FakeRpcTransport([
      {
        method: "workflow.list",
        result: {
          project_id: "project-1",
          workflows: [
            {
              id: "11111111-1111-4111-8111-111111111111",
              name: "Delivery",
              description: "Ship",
              version: 4,
              execution_target_policy: { mode: "custom_ref", custom_ref: "release/v1" },
              project_link: { default: true },
            },
          ],
          next_offset: 10,
        },
      },
      {
        method: "workflow.create",
        result: {
          workflow: {
            id: "22222222-2222-4222-8222-222222222222",
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
            id: "33333333-3333-4333-8333-333333333333",
            name: "Project workflow",
            description: "",
            version: 1,
            execution_target_policy: { mode: "none" },
          },
          link: {
            id: "link-3",
            project_id: "project-1",
            workflow_id: "33333333-3333-4333-8333-333333333333",
            default: true,
          },
        },
      },
      {
        method: "workflow.linkProject",
        result: {
          link: {
            id: "link-1",
            project_id: "project-1",
            workflow_id: "11111111-1111-4111-8111-111111111111",
            default: false,
          },
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(
      client.listWorkflows({ offset: 0, limit: 10, projectID: "project-1", query: "ship" }),
    ).resolves.toMatchObject({
      nextOffset: 10,
      workflows: [
        {
          id: "11111111-1111-4111-8111-111111111111",
          name: "Delivery",
          version: 4,
          executionTargetPolicy: { mode: "custom_ref", customRef: "release/v1" },
          projectLink: { isDefault: true },
        },
      ],
    });
    await expect(client.createWorkflow({ name: "Ops", description: "" })).resolves.toMatchObject({
      id: "22222222-2222-4222-8222-222222222222",
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
      link: { isDefault: true, projectID: "project-1", workflowID: "33333333-3333-4333-8333-333333333333" },
      workflow: { id: "33333333-3333-4333-8333-333333333333" },
    });
    await expect(
      client.linkWorkflowToProject({
        projectID: "project-1",
        workflowID: "11111111-1111-4111-8111-111111111111",
      }),
    ).resolves.toMatchObject({
      id: "link-1",
      isDefault: false,
    });

    expect(transport.calls).toContainEqual({
      method: "workflow.list",
      params: { offset: 0, limit: 10, project_id: "project-1", query: "ship" },
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
        workflow_id: "11111111-1111-4111-8111-111111111111",
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

    await expect(client.previewWorkflowDelete("11111111-1111-4111-8111-111111111111")).resolves.toMatchObject(
      {
        workflowID: "11111111-1111-4111-8111-111111111111",
        version: 7,
        projectCount: 1,
        linkCount: 1,
        defaultReplacementProjectCount: 0,
        taskCount: 2,
        currentNodeCount: 0,
        pendingApprovalCount: 1,
        blockedTaskCount: 1,
      },
    );
    await expect(
      client.deleteWorkflow({
        workflowID: "11111111-1111-4111-8111-111111111111",
        confirmed: true,
        expectedVersion: 7,
        expectedProjectCount: 1,
        expectedLinkCount: 1,
        expectedTaskCount: 2,
        cleanupArtifacts: false,
      }),
    ).resolves.toMatchObject({
      deleted: false,
      blockers: [{ code: "pending_approvals", count: 1 }],
    });

    expect(transport.calls).toContainEqual({
      method: "workflow.deletePreview",
      params: { workflow_id: "11111111-1111-4111-8111-111111111111" },
    });
    expect(transport.calls).toContainEqual({
      method: "workflow.delete",
      params: {
        workflow_id: "11111111-1111-4111-8111-111111111111",
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
                edge_id: workflowGraphDraftIDs.startEdge,
                input_bindings: [{ name: "brief", source: "transition_output", field: "brief" }],
                required_provision_fields: [{ name: "brief", description: "Brief" }],
                assignee_selection_applicability: {
                  available: true,
                  parameter_visible: true,
                  reason: "eligible",
                },
                thinking_selection_applicability: {
                  available: true,
                  parameter_visible: true,
                  reason: "eligible",
                },
              },
            ],
          },
        },
      },
      {
        method: "workflow.graph.savePreview",
        result: {
          changed: true,
          current_version: 11,
          validation_results: graphValidationResults,
          impact: workflowGraphSaveImpactResponse,
          blockers: [
            {
              code: "confirmation_required",
              message: "Confirm removal.",
              count: 1,
              affected_entities: [{ entity_type: "edge", entity_id: workflowGraphDraftIDs.startEdge }],
            },
          ],
          can_save: false,
          confirmation_required: true,
        },
      },
      {
        method: "workflow.graph.save",
        result: {
          saved: true,
          changed: true,
          definition: workflowDefinitionResponse.definition,
          current_version: 12,
          validation_results: graphValidationResults,
          impact: {
            ...workflowGraphSaveImpactResponse,
            removed_node_group_count: 0,
            removed_edge_count: 0,
            removed_entities: [],
          },
          blockers: null,
          can_save: true,
          confirmation_required: false,
        },
      },
    ]);
    const client = new ApiClient(transport);

    await expect(
      client.validateWorkflowGraphDraft({
        workflowID: "11111111-1111-4111-8111-111111111111",
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
            edgeID: workflowGraphDraftIDs.startEdge,
            inputBindings: [{ field: "brief", name: "brief", source: "transition_output" }],
            requiredProvisionFields: [{ description: "Brief", name: "brief" }],
          },
        ],
      },
    });
    await expect(
      client.previewWorkflowGraphSave({
        workflowID: "11111111-1111-4111-8111-111111111111",
        expectedVersion: 11,
        metadata: {
          name: "Preview Workflow",
          description: "Preview description",
          executionTargetPolicy: { mode: "default_branch", customRef: null },
        },
        graph: workflowGraphDraft,
      }),
    ).resolves.toMatchObject({
      changed: true,
      currentVersion: 11,
      confirmationRequired: true,
      impact: { removedEdgeCount: 1 },
      blockers: [{ code: "confirmation_required" }],
    });
    await expect(
      client.saveWorkflowGraph({
        workflowID: "11111111-1111-4111-8111-111111111111",
        expectedVersion: 11,
        metadata: {
          name: "Saved Workflow",
          description: "Saved description",
          executionTargetPolicy: { mode: "none", customRef: null },
        },
        graph: workflowGraphDraft,
        confirmation: {
          expectedRemovedNodeGroupCount: 1,
          expectedRemovedNodeCount: 0,
          expectedRemovedTransitionGroupCount: 0,
          expectedRemovedEdgeCount: 1,
          expectedNodeTaskReferenceCount: 0,
          expectedEdgeTaskReferenceCount: 0,
        },
      }),
    ).resolves.toMatchObject({
      saved: true,
      changed: true,
      currentVersion: 12,
      definition: { workflow: { id: "11111111-1111-4111-8111-111111111111" } },
      blockers: [],
    });

    expect(transport.calls[0]).toEqual({
      method: "workflow.graph.validateDraft",
      params: {
        workflow_id: "11111111-1111-4111-8111-111111111111",
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
              id: workflowGraphDraftIDs.startNode,
              key: "backlog",
              kind: "start",
              display_name: "Backlog",
              group_id: null,
              join_input_providers: [],
            },
          ],
          transition_groups: [
            {
              id: workflowGraphDraftIDs.startTransitionGroup,
              source_node_id: workflowGraphDraftIDs.startNode,
              transition_id: "start",
              display_name: "Start",
              description: "Start the workflow.",
            },
          ],
          edges: [
            {
              id: workflowGraphDraftIDs.startEdge,
              transition_group_id: workflowGraphDraftIDs.startTransitionGroup,
              key: "start",
              target_node_id: workflowGraphDraftIDs.agentNode,
              assignee_selection: "configured",
              thinking_selection: "configured",
              requires_approval: false,
              context_mode: "new_session",
              context_source: { kind: "immediate_source", node_key: "" },
              parameters: [{ description: "Brief", key: "brief", purpose: "ordinary" }],
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
          expected_removed_node_group_count: 1,
          expected_removed_edge_count: 1,
        },
      },
    });
  });
});

const emptyBoardResponse = {
  board: {
    project_id: "project-1",
    project: {
      project_key: "proj",
      display_name: "Project",
      default_workspace_id: "workspace-1",
      attached_workspace_count: 1,
    },
    workflows: null,
    groups: null,
    columns: null,
    generated_at_unix_ms: 1,
  },
};

const boardWithJoinResponse = {
  board: {
    ...emptyBoardResponse.board,
    selected_workflow: {
      workflow_id: "11111111-1111-4111-8111-111111111111",
      display_name: "Workflow",
      description: "",
      version: 1,
      is_project_default: true,
      valid_for_task_creation: true,
      validation_errors: [],
    },
    groups: [
      {
        group_id: boundaryGraphIDs.nodeGroup,
        key: "review",
        display_name: "Review",
        sort_order: 1,
        node_ids: [boundaryGraphIDs.node, boundaryGraphIDs.joinNode],
      },
      {
        group_id: boundaryGraphIDs.joinOnlyNodeGroup,
        key: "join_only",
        display_name: "Join Only",
        sort_order: 2,
        node_ids: [boundaryGraphIDs.joinNode],
      },
    ],
    columns: [
      boardColumnResponse(boundaryGraphIDs.node, "agent"),
      boardColumnResponse(boundaryGraphIDs.joinNode, "join"),
    ],
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
    },
    group_id: boundaryGraphIDs.nodeGroup,
    sort_order: 1,
    is_backlog: false,
    is_done: false,
    task_count: 0,
  };
}

const emptyBoardNodeCardsResponse = {
  project_id: "project-1",
  workflow_id: "11111111-1111-4111-8111-111111111111",
  node_id: "node-1",
  cards: null,
  next_offset: 50,
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

const emptyTaskDetailResponse = {
  task: {
    summary: {
      id: "task-1",
      project_id: "project-1",
      workflow_id: "11111111-1111-4111-8111-111111111111",
      short_id: "PROJ-1",
      title: "Task",
      created_at_unix_ms: 1,
      updated_at_unix_ms: 1,
      done: false,
    },
    project: {
      display_name: "Project",
    },
    workflow: {
      workflow_id: "11111111-1111-4111-8111-111111111111",
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
      attention_types: [],
    },
    actions: {
      can_start: true,
      can_interrupt: false,
      can_resume: false,
      can_delete: true,
    },
    label_ids: ["f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf"],
    attention_count: 0,
    dependencies: {
      blocker_count: 0,
      unsatisfied_blocker_count: 0,
      directly_blocked_task_count: 0,
      directions: [
        {
          direction: "blocked-by",
          total_count: 0,
          unsatisfied_count: 0,
          items: [],
          add_availability: { available: { remaining_capacity: 5 } },
        },
        {
          direction: "blocks",
          total_count: 0,
          items: [],
          add_availability: { available: { remaining_capacity: 4 } },
        },
      ],
    },
    worktree_path: null,
    current_nodes: [],
    live_sessions: [],
    current_scripts: [],
    retained_session_count: 0,
  },
};

const workflowDefinitionResponse = {
  definition: {
    workflow: {
      id: "11111111-1111-4111-8111-111111111111",
      name: "Delivery",
      description: "Delivery workflow",
      version: 9,
      execution_target_policy: { mode: "head" },
    },
    node_groups: [
      {
        group_id: boundaryGraphIDs.nodeGroup,
        workflow_id: "11111111-1111-4111-8111-111111111111",
        group_key: "core",
        display_name: "Core",
        sort_order: 1,
      },
    ],
    nodes: [
      {
        id: boundaryGraphIDs.node,
        workflow_id: "11111111-1111-4111-8111-111111111111",
        key: "implement",
        kind: "agent",
        display_name: "Implement",
        group_id: boundaryGraphIDs.nodeGroup,
        group_key: "core",
        subagent_role: "coder",
      },
      {
        id: boundaryGraphIDs.doneNode,
        workflow_id: "11111111-1111-4111-8111-111111111111",
        key: "done",
        kind: "terminal",
        display_name: "Done",
        group_id: null,
      },
    ],
    transition_groups: [
      {
        id: boundaryGraphIDs.transitionGroup,
        workflow_id: "11111111-1111-4111-8111-111111111111",
        source_node_id: boundaryGraphIDs.node,
        transition_id: "done",
        display_name: "Done",
        description: "Choose this when implementation is complete.",
      },
    ],
    edges: [
      {
        id: boundaryGraphIDs.edge,
        workflow_id: "11111111-1111-4111-8111-111111111111",
        transition_group_id: boundaryGraphIDs.transitionGroup,
        key: "done",
        target_node_id: boundaryGraphIDs.doneNode,
        assignee_selection: "configured",
        thinking_selection: "configured",
        requires_approval: false,
        context_mode: "new_session",
        context_source: {
          kind: "selected_node",
          node_key: "implement",
        },
        prompt_template: "Summarize the implementation.",
        parameters: [{ key: "summary", description: "Summary", purpose: "ordinary" }],
        input_bindings: null,
        output_requirements: null,
      },
    ],
    derived_wiring: {
      nodes: [
        {
          node_id: boundaryGraphIDs.node,
          possible_provision_fields: [{ name: "summary", description: "Summary" }],
        },
      ],
      transition_groups: [
        {
          transition_group_id: boundaryGraphIDs.transitionGroup,
          required_provision_fields: [{ name: "summary", description: "Summary" }],
        },
      ],
      edges: [
        {
          edge_id: boundaryGraphIDs.edge,
          input_bindings: [{ name: "summary", source: "transition_output", field: "summary" }],
          required_provision_fields: [{ name: "summary", description: "Summary" }],
          assignee_selection_applicability: { available: true, parameter_visible: true, reason: "eligible" },
          thinking_selection_applicability: { available: true, parameter_visible: true, reason: "eligible" },
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
      workflow_id: "11111111-1111-4111-8111-111111111111",
      node_id: boundaryGraphIDs.node,
      transition_group_id: boundaryGraphIDs.transitionGroup,
      edge_id: boundaryGraphIDs.edge,
      details: {
        input_name: "summary",
        placeholder: ".Params.summary",
        provider_edge_id: null,
      },
      related_ids: [boundaryGraphIDs.relatedEdge],
      blocks_context: true,
    },
  ],
};

const workflowLinksResponse = {
  links: [
    {
      id: "link-1",
      project_id: "project-1",
      workflow_id: "11111111-1111-4111-8111-111111111111",
      default: true,
    },
  ],
};

const workflowDeleteImpactResponse = {
  workflow_id: "11111111-1111-4111-8111-111111111111",
  version: 7,
  project_count: 1,
  link_count: 1,
  default_replacement_project_count: 0,
  task_count: 2,
  current_node_count: 0,
  pending_approval_count: 1,
  blocked_task_count: 1,
};

const workflowDeletePreviewResponse = {
  impact: workflowDeleteImpactResponse,
};

const workflowDeleteResponse = {
  deleted: false,
  impact: workflowDeleteImpactResponse,
  blockers: [{ code: "pending_approvals", message: "Workflow has pending approvals.", count: 1 }],
};

const workflowGraphSaveImpactResponse = {
  removed_node_group_count: 1,
  removed_node_count: 0,
  removed_transition_group_count: 0,
  removed_edge_count: 1,
  removed_entities: [
    { entity_type: "edge", entity_id: workflowGraphDraftIDs.startEdge },
    { entity_type: "node_group", entity_id: boundaryGraphIDs.nodeGroup },
  ],
  node_task_reference_count: 0,
  edge_task_reference_count: 0,
  active_current_node_count: 0,
  pending_approval_count: 0,
  start_node_change_count: 0,
  last_terminal_change_count: 0,
  task_referenced_node_kind_change_count: 0,
};
