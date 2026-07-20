import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import { ActionableListRow } from "./index";

describe("ActionableListRow", () => {
  it("keeps row selection separate from keyboard-reachable actions", async () => {
    const onSelect = vi.fn();
    const onRename = vi.fn();
    const onDelete = vi.fn();
    const user = userEvent.setup();

    render(
      <ActionableListRow
        actions={
          <button aria-label="Rename" onClick={onRename} type="button">
            Edit
          </button>
        }
        contextualActions={
          <button aria-label="Delete" onClick={onDelete} type="button">
            Delete
          </button>
        }
        selected
        selectButtonProps={{ onClick: onSelect }}
      >
        Priority
      </ActionableListRow>,
    );

    const row = screen.getByRole("button", { name: "Priority" });
    expect(row).toHaveAttribute("aria-pressed", "true");

    row.focus();
    await user.keyboard("{Enter}");
    await user.click(screen.getByRole("button", { name: "Rename" }));
    await user.click(screen.getByRole("button", { name: "Delete" }));

    expect(onSelect).toHaveBeenCalledOnce();
    expect(onRename).toHaveBeenCalledOnce();
    expect(onDelete).toHaveBeenCalledOnce();
  });
});
