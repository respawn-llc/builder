import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import { InteractiveChip, ProgressChip, ProgressInteractiveChip } from "./index";

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

  it("composes semantic progress inside the native chip button", async () => {
    const onClick = vi.fn();
    const user = userEvent.setup();

    render(
      <ProgressInteractiveChip
        label="Dependency progress"
        maximum={4}
        onClick={onClick}
        tone="success"
        value={3}
      />,
    );

    const progress = screen.getByRole("progressbar", { name: "Dependency progress" });
    expect(progress).toHaveAttribute("aria-valuenow", "3");
    expect(progress).toHaveAttribute("aria-valuemax", "4");

    await user.click(screen.getByRole("button", { name: "Dependency progress" }));
    expect(onClick).toHaveBeenCalledOnce();
  });

  it("composes semantic progress without interactive semantics", () => {
    render(<ProgressChip label="Dependency progress" maximum={4} value={3} />);

    const progress = screen.getByRole("progressbar", { name: "Dependency progress" });
    expect(progress.closest("button")).toBeNull();
  });
});
