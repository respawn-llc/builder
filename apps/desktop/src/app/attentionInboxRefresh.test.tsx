import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, vi } from "vitest";

import { removeBrowserStorage } from "@/app-facade";
import { createTestServices, startupRoutes } from "@/test-support/app-services";
import { installAnimationFrameTestSupport } from "@/test-support/scheduling";
import { AppRoot } from "./AppRoot";

describe("open Inbox attention refresh", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/");
    clearRoutePersistence();
    installAnimationFrameTestSupport();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    window.history.replaceState(null, "", "/");
    clearRoutePersistence();
  });

  it("shows newly arrived authoritative attention without navigation or manual refresh", async () => {
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.attention.list",
        handler: (_params, callIndex) => attentionResponse(callIndex === 0 ? [] : [questionAttentionItem]),
      },
    ]);

    render(<AppRoot services={services} />);

    await waitFor(() => {
      expect(screen.getByTestId("home-route-root")).toBeInTheDocument();
      expect(attentionListCallCount(services)).toBe(1);
      expect(screen.queryByTestId("attention-row")).not.toBeInTheDocument();
    });

    act(() => {
      services.transport.emit("attention.notification", pendingQuestionEvent);
    });

    await waitFor(() => {
      expect(attentionListCallCount(services)).toBe(2);
      expect(screen.getByTestId("attention-row")).toHaveTextContent("Needs answer");
    });
  });

  it("removes resolved attention while Inbox remains open", async () => {
    const services = createTestServices([
      ...startupRoutes,
      {
        method: "workflow.attention.list",
        handler: (_params, callIndex) => attentionResponse(callIndex === 0 ? [questionAttentionItem] : []),
      },
    ]);

    render(<AppRoot services={services} />);

    await waitFor(() => {
      expect(screen.getByTestId("attention-row")).toHaveTextContent("Needs answer");
      expect(attentionListCallCount(services)).toBe(1);
    });

    act(() => {
      services.transport.emit("attention.notification", {
        event: {
          type: "resolved",
          sequence: 1,
          source: "live",
          id: { kind: "question", uuid: "step-1" },
          kind: "question",
          occurred_at: "2026-08-28T20:01:00Z",
        },
      });
    });

    await waitFor(() => {
      expect(attentionListCallCount(services)).toBe(2);
      expect(screen.queryByTestId("attention-row")).not.toBeInTheDocument();
    });
  });
});

function attentionListCallCount(services: ReturnType<typeof createTestServices>): number {
  return services.transport.calls.filter((call) => call.method === "workflow.attention.list").length;
}

function attentionResponse(items: readonly (typeof questionAttentionItem)[]) {
  return {
    items,
    next_page_token: "",
    generated_at_unix_ms: 1,
  };
}

function clearRoutePersistence(): void {
  removeBrowserStorage("local", "desktop.lastProjectRoute");
  removeBrowserStorage("session", "desktop.routeRestoreChecked");
}

const workflowID = "11111111-1111-4111-8111-111111111111";

const pendingQuestionEvent = {
  event: {
    type: "pending",
    sequence: 1,
    source: "live",
    pending: {
      id: { kind: "question", uuid: "step-1" },
      kind: "question",
      occurred_at: "2026-08-28T20:00:00Z",
      revision: 1,
      question: {
        prepared_ask_ids: ["ask-1"],
        materialized_ask_ids: ["ask-1"],
        current_unresolved_ask_ids: ["ask-1"],
        skipped_ask_ids: [],
        preview: "Question from agent",
        display_count: 1,
        materialized_count: 1,
      },
      target: {
        kind: "workflow_task",
        project_id: "project-1",
        workflow_id: workflowID,
        task_id: "task-1",
        task_short_id: "KT-1",
        task_title: "Needs answer",
        session_id: "session-1",
        current_node_id: "node-1",
        focus: { kind: "question", ask_ids: ["ask-1"] },
      },
    },
  },
} as const;

const questionAttentionItem = {
  id: "question:node-1:ask-1",
  kind: "question",
  project_id: "project-1",
  workflow_id: workflowID,
  task_id: "task-1",
  task_short_id: "KT-1",
  task_title: "Needs answer",
  current_node: { node_id: "node-1", transition_branch_key: null, session_id: null },
  session_name: "Session one",
  message: "Question from agent",
  question: {
    session_id: "session-1",
    step_id: "22222222-2222-4222-8222-222222222222",
    prompt_id: "ask-1",
    kind: "ordinary",
    suggestions: [],
    recommended_option_index: null,
  },
  occurred_at_unix_ms: 1,
} as const;
