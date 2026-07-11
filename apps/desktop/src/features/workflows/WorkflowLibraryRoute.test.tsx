import {
  createBrowserNativeBridge,
  type NativeBridge,
  type NativeDialogWindowOptions,
} from "@app/native-bridge";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach } from "vitest";

import { App } from "../../App";
import { createTestServices, startupRoutes } from "../../testSupport/appServices";

describe("WorkflowLibraryRoute", () => {
  const originalResizeObserver = globalThis.ResizeObserver;

  beforeEach(() => {
    window.history.pushState(null, "", "/workflows");
    globalThis.ResizeObserver = MockResizeObserver;
  });

  afterEach(() => {
    globalThis.ResizeObserver = originalResizeObserver;
  });

  it("renders the empty workflow library without duplicate header controls", async () => {
    render(
      <App
        services={createTestServices([
          ...startupRoutes,
          { method: "workflow.list", result: { workflows: [], next_page_token: "" } },
        ])}
      />,
    );

    await screen.findByTestId("workflow-library-route");
    expect(screen.queryByRole("heading", { name: "Workflow Library" })).not.toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Create workflow" })).toHaveLength(1);
  });

  it("opens workflow editor in the sidebar from the workflow picker context menu", async () => {
    render(
      <App
        services={createTestServices([
          ...startupRoutes,
          { method: "workflow.list", result: workflowListResponse },
          { method: "workflow.get", result: workflowDefinitionResponse },
          { method: "workflow.validate", result: validWorkflowValidationResponse },
          { method: "workflow.graph.validateDraft", result: validWorkflowGraphValidationResponse },
        ])}
      />,
    );

    fireEvent.contextMenu(await screen.findByRole("button", { name: "Delivery rev 1" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Edit" }));

    const sidebar = await screen.findByRole("complementary", { name: "Workflow editor" });
    expect(await within(sidebar).findByTestId("workflow-editor-route")).toBeInTheDocument();
    expect(await within(sidebar).findByTestId("workflow-editor-canvas")).toBeInTheDocument();
    fireEvent.click(within(sidebar).getByRole("button", { name: "Inspect workflow" }));
    expect(
      await within(sidebar).findByRole("complementary", { name: "Inspect workflow" }),
    ).toBeInTheDocument();
    expect(within(sidebar).getByTestId("workflow-editor-route")).toBeInTheDocument();
    expect(window.location.pathname).toBe("/workflows");
  });

  it("refreshes the mounted workflow list after saving from the sidebar editor", async () => {
    const user = userEvent.setup();
    let saved = false;
    const updatedWorkflowDefinitionResponse = workflowDefinitionResponseWithMetadata({
      description: "Ship changes faster",
      name: "Delivery Updated",
      version: 2,
    });
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.list",
        handler: () =>
          saved
            ? workflowListResponseWithMetadata({
                description: "Ship changes faster",
                name: "Delivery Updated",
                version: 2,
              })
            : workflowListResponse,
      },
      {
        method: "workflow.get",
        handler: () => (saved ? updatedWorkflowDefinitionResponse : workflowDefinitionResponse),
      },
      { method: "workflow.validate", result: validWorkflowValidationResponse },
      { method: "workflow.graph.validateDraft", result: validWorkflowGraphValidationResponse },
      { method: "workflow.graph.savePreview", result: graphSavePreviewResponse },
      {
        method: "workflow.graph.save",
        handler: () => {
          saved = true;
          return {
            ...graphSavePreviewResponse,
            current_version: 2,
            definition: updatedWorkflowDefinitionResponse.definition,
            saved: true,
          };
        },
      },
    ]);
    render(
      <App services={services} />,
    );

    fireEvent.contextMenu(await screen.findByRole("button", { name: "Delivery rev 1" }));
    await user.click(await screen.findByRole("menuitem", { name: "Edit" }));

    const sidebar = await screen.findByRole("complementary", { name: "Workflow editor" });
    await user.click(await within(sidebar).findByRole("button", { name: "Inspect workflow" }));
    const inspector = await within(sidebar).findByRole("complementary", { name: "Inspect workflow" });
    await user.clear(within(inspector).getByLabelText("Workflow name"));
    await user.type(within(inspector).getByLabelText("Workflow name"), "Delivery Updated");
    await user.click(within(inspector).getByRole("button", { name: "Close" }));
    await user.click(within(sidebar).getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("button", { name: "Delivery Updated rev 2" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delivery rev 1" })).not.toBeInTheDocument();
  }, 10000);

  it("opens existing workflow delete confirmation flow from the workflow picker context menu", async () => {
    const opened: NativeDialogWindowOptions[] = [];
    const services = createTestServices(
      [
        ...startupRoutes,
        { method: "workflow.list", result: workflowListResponse },
        { method: "workflow.deletePreview", result: workflowDeletePreviewResponse },
      ],
      nativeWorkflowDeleteDialogBridge(opened),
    );

    render(<App services={services} />);

    fireEvent.contextMenu(await screen.findByRole("button", { name: "Delivery rev 1" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete" }));

    await waitFor(() => {
      expect(opened).toHaveLength(1);
    });
    expect(opened[0]).toMatchObject({
      initialHeight: 300,
      initialWidth: 460,
      route: "/native-dialog/workflow-delete",
      title: "Delete workflow?",
      params: {
        active_run_count: "0",
        blocked_task_count: "0",
        default_replacement_project_count: "0",
        link_count: "1",
        project_count: "1",
        runnable_run_count: "0",
        task_count: "2",
        version: "1",
        workflow_id: "workflow-1",
      },
    });
    expect(services.transport.calls.map((call) => call.method)).toContain("workflow.deletePreview");
    expect(screen.queryByRole("dialog", { name: "Delete workflow?" })).not.toBeInTheDocument();
  });
});

class MockResizeObserver implements ResizeObserver {
  observe(): void {
    return;
  }

  unobserve(): void {
    return;
  }

  disconnect(): void {
    return;
  }
}

function nativeWorkflowDeleteDialogBridge(opened: NativeDialogWindowOptions[]): NativeBridge {
  const base = createBrowserNativeBridge();
  return {
    ...base,
    capabilities: {
      ...base.capabilities,
      dialogWindows: true,
    },
    dialogs: {
      ...base.dialogs,
      async openWindow(options): Promise<void> {
        opened.push(options);
      },
    },
  };
}

const workflowListResponse = {
  workflows: [
    {
      id: "workflow-1",
      name: "Delivery",
      description: "Ship changes",
      execution_policy: { mode: "ask" },
      version: 1,
    },
  ],
  next_page_token: "",
};

function workflowListResponseWithMetadata({
  description,
  name,
  version,
}: Readonly<{ description: string; name: string; version: number }>) {
  return {
    workflows: [
      {
        id: "workflow-1",
        name,
        description,
        execution_policy: { mode: "ask" },
        version,
      },
    ],
    next_page_token: "",
  };
}

const workflowDefinitionResponse = {
  definition: {
    workflow: {
      id: "workflow-1",
      name: "Delivery",
      description: "Ship changes",
      execution_policy: { mode: "ask" },
      version: 1,
    },
    node_groups: [],
    nodes: [
      {
        id: "backlog",
        workflow_id: "workflow-1",
        key: "backlog",
        kind: "agent",
        display_name: "Backlog",
        group_id: "",
        group_key: "",
        subagent_role: "default",
        prompt_template: "",
        input_fields: [],
        join_input_providers: [],
        output_fields: [],
      },
      {
        id: "done",
        workflow_id: "workflow-1",
        key: "done",
        kind: "terminal",
        display_name: "Done",
        group_id: "",
        group_key: "",
        subagent_role: "",
        prompt_template: "",
        input_fields: [],
        join_input_providers: [],
        output_fields: [],
      },
    ],
    transition_groups: [
      {
        id: "transition-backlog-done",
        workflow_id: "workflow-1",
        source_node_id: "backlog",
        transition_id: "done",
        display_name: "Done",
      },
    ],
    edges: [
      {
        id: "edge-backlog-done",
        workflow_id: "workflow-1",
        transition_group_id: "transition-backlog-done",
        key: "done",
        target_node_id: "done",
        requires_approval: false,
        context_mode: "new_session",
        context_source: { kind: "immediate_source" },
        input_bindings: [],
        output_requirements: [],
      },
    ],
    derived_wiring: {
      diagnostics: [],
      edges: [],
      nodes: [],
      transition_groups: [],
    },
  },
};

function workflowDefinitionResponseWithMetadata({
  description,
  name,
  version,
}: Readonly<{ description: string; name: string; version: number }>) {
  return {
    definition: {
      ...workflowDefinitionResponse.definition,
      workflow: {
        ...workflowDefinitionResponse.definition.workflow,
        description,
        name,
        version,
      },
    },
  };
}

const validWorkflowValidationResponse = {
  valid: true,
  errors: [],
};

const validWorkflowGraphValidationResponse = {
  results: {
    draft: validWorkflowValidationResponse,
    execution: validWorkflowValidationResponse,
  },
  derived_wiring: {
    diagnostics: [],
    edges: [],
    nodes: [],
    transition_groups: [],
  },
};

const graphSaveImpactResponse = {
  removed_node_count: 0,
  removed_transition_group_count: 0,
  removed_edge_count: 0,
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

const graphSavePreviewResponse = {
  current_version: 1,
  validation_results: validWorkflowGraphValidationResponse.results,
  impact: graphSaveImpactResponse,
  blockers: [],
  can_save: true,
  confirmation_required: false,
};

const workflowDeletePreviewResponse = {
  impact: {
    workflow_id: "workflow-1",
    version: 1,
    project_count: 1,
    link_count: 1,
    default_replacement_project_count: 0,
    task_count: 2,
    active_run_count: 0,
    runnable_run_count: 0,
    blocked_task_count: 0,
  },
};
