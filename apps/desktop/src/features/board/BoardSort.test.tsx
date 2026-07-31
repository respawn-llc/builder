import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, vi } from "vitest";

import { BoardSortChrome } from "./BoardSort";

const boardMocks = vi.hoisted(() => ({
  controller: {
    setDesiredSort: vi.fn(),
  },
  sort: { field: "updated", direction: "desc" },
}));

vi.mock("./BoardFilterGenerationRuntime", () => ({
  useBoardFilterGeneration: () => ({
    controller: boardMocks.controller,
    snapshot: {
      active: {
        filter: { kind: "none" as const },
        generation: 1,
        retiring: false,
        sort: boardMocks.sort,
      },
      desiredFilter: null,
      desiredSort: null,
    },
  }),
}));

describe("BoardSortChrome", () => {
  beforeEach(() => {
    boardMocks.sort = { field: "updated", direction: "desc" };
    boardMocks.controller.setDesiredSort.mockClear();
  });

  it("opens with one selected direction and one selected field at the default sort", async () => {
    const user = userEvent.setup();
    render(<BoardSortChrome />);

    const trigger = sortTrigger();
    expect(trigger).toHaveAttribute("aria-pressed", "false");
    expect(trigger.getAttribute("aria-label") ?? trigger.textContent).not.toHaveLength(0);

    await user.click(trigger);

    expect(screen.getAllByRole("radiogroup")).toHaveLength(2);
    expect(screen.getAllByRole("radio", { checked: true })).toHaveLength(2);
  });

  it("applies field and direction changes immediately while keeping the popover open", async () => {
    const user = userEvent.setup();
    render(<BoardSortChrome />);
    await user.click(sortTrigger());

    const groups = screen.getAllByRole("radiogroup");
    const directionGroup = groups[0];
    const fieldGroup = groups[1];
    if (directionGroup === undefined || fieldGroup === undefined) {
      throw new Error("Board Sort radio groups did not render.");
    }
    const fields = within(fieldGroup).getAllByRole("radio");
    const directions = within(directionGroup).getAllByRole("radio");
    const titleField = fields[3];
    const ascendingDirection = directions[0];
    if (titleField === undefined || ascendingDirection === undefined) {
      throw new Error("Board Sort radio options did not render.");
    }
    await user.click(titleField);
    await user.click(ascendingDirection);

    expect(boardMocks.controller.setDesiredSort).toHaveBeenNthCalledWith(1, {
      direction: "desc",
      field: "title",
    });
    expect(boardMocks.controller.setDesiredSort).toHaveBeenNthCalledWith(2, {
      direction: "asc",
      field: "updated",
    });
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("derives active state from a non-default structured sort and dismisses normally", async () => {
    const user = userEvent.setup();
    const view = render(<BoardSortChrome />);
    const defaultName = sortTrigger().getAttribute("aria-label") ?? sortTrigger().textContent;
    view.unmount();
    boardMocks.sort = { field: "labels", direction: "asc" };
    render(<BoardSortChrome />);

    const trigger = sortTrigger();
    expect(trigger).toHaveAttribute("aria-pressed", "true");
    expect(trigger.getAttribute("aria-label") ?? trigger.textContent).not.toBe(defaultName);

    await user.click(trigger);
    await user.keyboard("{Escape}");

    expect(trigger).toHaveAttribute("aria-expanded", "false");
  });
});

function sortTrigger(): HTMLElement {
  const buttons = screen.getAllByRole("button");
  const trigger = buttons.find((button) => button.hasAttribute("aria-pressed"));
  if (trigger === undefined) {
    throw new Error("Board Sort trigger did not render.");
  }
  return trigger;
}
