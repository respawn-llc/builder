import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { z } from "zod";

import type { JsonValue } from "../../api/json";
import { App } from "../../App";
import { TestDataTransfer } from "../../test-support/board/TestDataTransfer";
import { createTestServices, startupRoutes } from "../../testSupport/appServices";

describe("BoardRoute native drag lifecycle", () => {
  beforeEach(() => {
    window.history.pushState(null, "", "/projects/project-1?workflowId=workflow-1");
  });

  it.each([
    ["document drop", () => document.dispatchEvent(new Event("drop", { bubbles: true, cancelable: true }))],
    ["card dragend", (source: HTMLElement) => fireEvent.dragEnd(source)],
    ["document dragend", () => document.dispatchEvent(new Event("dragend", { bubbles: true }))],
    [
      "document exit/cancel",
      () => {
        const event = new Event("dragleave", { bubbles: true });
        Object.defineProperty(event, "relatedTarget", { value: null });
        document.dispatchEvent(event);
      },
    ],
    ["window blur", () => window.dispatchEvent(new Event("blur"))],
  ])("releases the source pin and active drag on %s", async (_termination, terminate) => {
    let sourcePresent = true;
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.board.get",
        handler: () => boardResponse(sourcePresent ? 1 : 0),
      },
      {
        method: "workflow.board.nodeCards.list",
        handler: (params: JsonValue) =>
          nodeCardsResponse(nodeID(params), sourcePresent && nodeID(params) === "backlog"),
      },
    ]);
    render(<App services={services} />);

    const source = await screen.findByRole("article", { name: "Drag source" });
    const destination = screen.getByRole("listitem", { name: "Recon" });
    fireEvent.dragStart(source, { dataTransfer: new TestDataTransfer() });
    await waitFor(() => {
      expect(destination).toHaveAttribute("data-drop-state", "allowed");
    });

    await act(async () => {
      terminate(source);
      await Promise.resolve();
    });

    await waitFor(() => {
      expect(screen.getByRole("listitem", { name: "Backlog" })).toHaveAttribute("data-drop-state", "idle");
      expect(destination).toHaveAttribute("data-drop-state", "idle");
    });

    sourcePresent = false;
    services.transport.emit("workflow.project", {
      event: {
        action: "updated",
        changed_ids: ["task-1"],
        project_id: "project-1",
        resource: "task",
        workflow_id: "workflow-1",
      },
    });

    await waitFor(() => {
      expect(screen.queryByRole("article", { name: "Drag source" })).not.toBeInTheDocument();
    });
  });
});

function nodeID(params: JsonValue): string {
  return nodeCardsRequestSchema.parse(params).node_id;
}

function boardResponse(backlogTaskCount: number) {
  return {
    board: {
      project_id: "project-1",
      project: {
        project_key: "KNT",
        display_name: "Kent",
        default_workspace_id: "workspace-1",
        attached_workspace_count: 1,
      },
      selected_workflow: workflow,
      workflows: [workflow],
      groups: [],
      columns: [
        boardColumn({
          id: "backlog",
          kind: "backlog",
          name: "Backlog",
          sortOrder: 0,
          isBacklog: true,
          taskCount: backlogTaskCount,
        }),
        boardColumn({
          id: "recon",
          kind: "agent",
          name: "Recon",
          sortOrder: 1,
          isBacklog: false,
          taskCount: 0,
        }),
      ],
      generated_at_unix_ms: 1,
    },
  };
}

function boardColumn(
  input: Readonly<{
    id: string;
    kind: string;
    name: string;
    sortOrder: number;
    isBacklog: boolean;
    taskCount: number;
  }>,
) {
  return {
    node: {
      node_id: input.id,
      key: input.id,
      kind: input.kind,
      display_name: input.name,
      assignee_role: "",
      output_fields: [],
      transition_output_fields: [],
    },
    group_id: "",
    sort_order: input.sortOrder,
    is_backlog: input.isBacklog,
    is_done: false,
    task_count: input.taskCount,
  };
}

function nodeCardsResponse(nodeIDValue: string, includeSource: boolean) {
  return {
    project_id: "project-1",
    workflow_id: "workflow-1",
    node_id: nodeIDValue,
    cards: includeSource ? [sourceCard] : [],
    previous_page_token: null,
    next_page_token: null,
    generated_at_unix_ms: 1,
  };
}

const workflow = {
  workflow_id: "workflow-1",
  display_name: "Workflow",
  description: "",
  version: 1,
  is_project_default: true,
  valid_for_task_creation: true,
  validation_errors: [],
};

const sourceCard = {
  task_id: "task-1",
  short_id: "KNT-1",
  title: "Drag source",
  preview: { markdown: "Source preview", truncated: false },
  workflow_id: "workflow-1",
  active_node_ids: ["backlog"],
  source_workspace: {
    workspace_id: "workspace-1",
    display_name: "Main",
    root_path: "/workspace/main",
    availability: "available",
    is_primary: true,
    updated_at_unix_ms: 1,
  },
  status: {
    kind: "backlog",
    native_state: "",
    node_ids: [],
    run_ids: [],
    attention_types: [],
  },
  actions: {
    can_start: false,
    can_interrupt: false,
    can_resume: false,
    can_cancel: true,
    manual_move_target_node_ids: ["recon"],
  },
  updated_at_unix_ms: 1,
};

const nodeCardsRequestSchema = z.object({ node_id: z.string() });
