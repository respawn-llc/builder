import { render, screen } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeAll, describe, expect, it } from "vitest";

import { appI18n, initializeI18n } from "@/i18n";
import { TaskCurrentNodeSelectionProperties } from "./TaskDetailRows";

beforeAll(async () => {
  await initializeI18n();
});

describe("TaskCurrentNodeSelectionProperties", () => {
  it("renders server-owned effective Agent selection", () => {
    render(
      <I18nextProvider i18n={appI18n}>
        <dl>
          <TaskCurrentNodeSelectionProperties
            node={{
              effectiveAssignee: "reviewer",
              effectiveThinking: "high",
              nodeID: "node-1",
              sessionID: null,
              transitionBranchKey: null,
            }}
          />
        </dl>
      </I18nextProvider>,
    );
    expect(screen.getByLabelText("Node node-1 effective assignee value")).toHaveTextContent("reviewer");
    expect(screen.getByLabelText("Node node-1 effective thinking value")).toHaveTextContent("high");
  });

  it("omits effective selection for non-Agent current nodes", () => {
    render(
      <I18nextProvider i18n={appI18n}>
        <dl>
          <TaskCurrentNodeSelectionProperties
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
});
