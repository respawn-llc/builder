import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import { BoardDependencyProgressChip } from "./BoardDependencyProgressChip";

describe("BoardDependencyProgressChip", () => {
  it("isolates chip activation from its card and exposes server progress", async () => {
    const onActivate = vi.fn();
    const onCardActivate = vi.fn();
    const user = userEvent.setup();

    render(
      <div onClick={onCardActivate}>
        <BoardDependencyProgressChip
          label="Dependency progress"
          onActivate={onActivate}
          progress={{ satisfiedCount: 1, totalCount: 2 }}
        />
      </div>,
    );

    await user.click(screen.getByRole("button", { name: "Dependency progress" }));

    expect(onActivate).toHaveBeenCalledOnce();
    expect(onCardActivate).not.toHaveBeenCalled();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "1");
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuemax", "2");
  });
});
