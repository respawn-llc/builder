import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import { MarkdownText } from "./MarkdownText";

describe("MarkdownText task lists", () => {
  it("toggles the selected task marker without rewriting surrounding Markdown", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    const value = "Before\n\n3. [X] **Keep this formatting**\n\nAfter";

    render(
      <MarkdownText
        onChange={onChange}
        taskListItemToggleLabel={() => "Toggle item"}
        value={value}
      />,
    );

    await user.click(screen.getByRole("checkbox"));

    expect(onChange).toHaveBeenCalledWith("Before\n\n3. [ ] **Keep this formatting**\n\nAfter");
  });
});
