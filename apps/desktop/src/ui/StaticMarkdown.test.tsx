import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import { StaticMarkdown } from "./MarkdownText";

describe("StaticMarkdown task lists", () => {
  it("replaces an editable checklist without using stale source offsets", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    const initialValue = "- [ ] Keep the Markdown source";
    const updatedValue = "New context\n\n- [ ] Keep the Markdown source";
    const view = render(
      <StaticMarkdown
        onTaskListChange={onChange}
        taskListItemToggleLabel={() => "Toggle item"}
        value={initialValue}
      />,
    );

    view.rerender(
      <StaticMarkdown
        onTaskListChange={onChange}
        taskListItemToggleLabel={() => "Toggle item"}
        value={updatedValue}
      />,
    );
    await user.click(screen.getByRole("checkbox"));

    expect(onChange).toHaveBeenCalledWith("New context\n\n- [x] Keep the Markdown source");
  });

  it("toggles the selected task marker without rewriting surrounding Markdown", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    const value = "Before\n\n3. [X] **Keep this formatting**\n\nAfter";

    render(
      <StaticMarkdown
        onTaskListChange={onChange}
        taskListItemToggleLabel={() => "Toggle item"}
        value={value}
      />,
    );

    await user.click(screen.getByRole("checkbox"));

    expect(onChange).toHaveBeenCalledWith("Before\n\n3. [ ] **Keep this formatting**\n\nAfter");
  });
});
