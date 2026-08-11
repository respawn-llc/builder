import { render, screen } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeAll, describe, expect, it } from "vitest";

import { appI18n, initializeI18n } from "@/i18n";
import { mountTaskDetailSurface, taskDetailResponse } from "@/test-support/task-detail";
import { TaskCurrentNodeSelectionProperties } from "./TaskDetailRows";

beforeAll(async () => {
  await initializeI18n();
});

describe("TaskCurrentNodeSelectionProperties", () => {
  it("renders a live Agent selection with the readable Node display name", () => {
    render(
      <I18nextProvider i18n={appI18n}>
        <dl>
          <TaskCurrentNodeSelectionProperties
            nodeDisplayName="Code Review"
            node={{
              effectiveAssignee: "reviewer",
              effectiveThinking: "high",
              nodeID: "workflow-node-b8c8f478-c5f7-44c4-a7f9-b0e7d1dfbdac",
              sessionID: null,
              transitionBranchKey: null,
            }}
          />
        </dl>
      </I18nextProvider>,
    );
    expect(screen.getByLabelText("Code Review node role value")).toHaveTextContent("reviewer");
    expect(screen.getByLabelText("Code Review node thinking value")).toHaveTextContent("high");
    expect(screen.queryByText(/workflow-node-b8c8f478/i)).toBeNull();
  });

  it("omits effective selection for non-Agent current nodes", () => {
    render(
      <I18nextProvider i18n={appI18n}>
        <dl>
          <TaskCurrentNodeSelectionProperties
            nodeDisplayName="Script"
            node={{
              effectiveAssignee: null,
              effectiveThinking: null,
              nodeID: "script-1",
              sessionID: null,
              transitionBranchKey: null,
            }}
          />
        </dl>
      </I18nextProvider>,
    );
    expect(screen.queryByText(/effective assignee/i)).toBeNull();
    expect(screen.queryByText(/effective thinking/i)).toBeNull();
  });

  it("shows selection metadata only for a Current Node with a live Agent execution", async () => {
    mountTaskDetailSurface({
      task: {
        ...taskDetailResponse.task,
        current_nodes: [
          {
            node_id: "workflow-node-live",
            transition_branch_key: null,
            session_id: "session-live",
            effective_assignee: "coder_low",
            effective_thinking: "high",
          },
          {
            node_id: "workflow-node-retained",
            transition_branch_key: "review",
            session_id: "session-retained",
            effective_assignee: "reviewer",
            effective_thinking: "xhigh",
          },
        ],
        live_sessions: [
          {
            session_id: "session-live",
            session_name: "Live review",
            node_display_name: "Code Review",
          },
        ],
      },
    });

    expect(await screen.findByLabelText("Code Review node role value")).toHaveTextContent("coder_low");
    expect(screen.getByLabelText("Code Review node thinking value")).toHaveTextContent("high");
    expect(screen.queryByText("reviewer")).toBeNull();
    expect(screen.queryByText("workflow-node-live")).toBeNull();
    expect(screen.queryByText("workflow-node-retained")).toBeNull();
  });
});
