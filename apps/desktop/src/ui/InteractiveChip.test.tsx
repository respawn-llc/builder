import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import { InteractiveChip } from "./index";

describe("InteractiveChip", () => {
  it("exposes toggle state and native keyboard activation", async () => {
    const onClick = vi.fn();
    const user = userEvent.setup();

    render(
      <InteractiveChip selected onClick={onClick}>
        Priority
      </InteractiveChip>,
    );

    const chip = screen.getByRole("button", { name: "Priority" });
    expect(chip).toHaveAttribute("aria-pressed", "true");

    chip.focus();
    await user.keyboard("{Enter}");
    await user.keyboard(" ");

    expect(chip).toHaveFocus();
    expect(onClick).toHaveBeenCalledTimes(2);
  });
});
