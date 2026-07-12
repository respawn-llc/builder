import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import { initializeI18n } from "../../i18n/setup";
import { workflowEditorEnglish } from "../../i18n/workflowEditorEn";
import { WorkflowNodeKindPicker } from "./WorkflowNodeKindPicker";

void initializeI18n();

describe("WorkflowNodeKindPicker", () => {
  it("reports a pointer choice from its single ordered node catalog", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(
      <WorkflowNodeKindPicker
        onSelect={onSelect}
        trigger={<button type="button">Open picker</button>}
        triggerPolicy="activation"
      />,
    );

    await user.click(screen.getByRole("button", { name: "Open picker" }));
    await user.click(screen.getByRole("button", { name: workflowEditorEnglish.addAgentNode }));

    expect(onSelect).toHaveBeenCalledExactlyOnceWith("agent", "pointer");
  });

  it("moves keyboard activation into the choices and reports keyboard selection", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(
      <WorkflowNodeKindPicker
        onSelect={onSelect}
        trigger={<button type="button">Open picker</button>}
        triggerPolicy="activation"
      />,
    );

    await user.tab();
    await user.keyboard("{Enter}");
    await user.keyboard("{Enter}");

    expect(onSelect).toHaveBeenCalledExactlyOnceWith("agent", "keyboard");
  });
});
