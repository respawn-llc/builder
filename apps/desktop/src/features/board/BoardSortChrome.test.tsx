import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

const runtime = vi.hoisted(() => ({
  sort: { field: "updated", direction: "desc" } as {
    field: "created" | "updated" | "labels" | "short_id";
    direction: "asc" | "desc";
  },
  setSort: vi.fn(),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}));

vi.mock("./BoardFilterGenerationRuntime", () => ({
  useBoardFilterGeneration: () => runtime,
}));

import { boardSortFieldOptions, BoardSortChrome } from "./BoardSortChrome";

describe("BoardSortChrome", () => {
  beforeEach(() => {
    runtime.sort = { field: "updated", direction: "desc" };
    runtime.setSort.mockReset();
  });

  it("shows the neutral default, exact field order, and both directions", async () => {
    const user = userEvent.setup();
    render(<BoardSortChrome />);

    expect(screen.getByTestId("board-sort-summary")).toHaveTextContent("board.sort.chip");
    await user.click(screen.getByTestId("board-sort-trigger"));

    const content = screen.getByRole("dialog");
    expect(
      within(content)
        .getAllByRole("radio")
        .slice(0, boardSortFieldOptions.length)
        .map((radio) => radio.getAttribute("value")),
    ).toEqual(boardSortFieldOptions.map((option) => option.value));
    expect(within(content).getByRole("radio", { name: "board.sort.directions.asc" })).toBeInTheDocument();
    expect(within(content).getByRole("radio", { name: "board.sort.directions.desc" })).toBeInTheDocument();
    expect(within(content).queryByRole("button", { name: "board.sort.apply" })).not.toBeInTheDocument();
    expect(within(content).queryByRole("button", { name: "board.sort.reset" })).not.toBeInTheDocument();
  });

  it("applies field and direction changes immediately while retaining the popover", async () => {
    const user = userEvent.setup();
    const view = render(<BoardSortChrome />);
    await user.click(screen.getByTestId("board-sort-trigger"));

    const content = screen.getByRole("dialog");
    await user.click(within(content).getByRole("radio", { name: "board.sort.fields.created" }));
    expect(runtime.setSort).toHaveBeenCalledWith({ field: "created", direction: "desc" });
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    runtime.sort = { field: "created", direction: "desc" };
    view.rerender(<BoardSortChrome />);
    await user.click(within(content).getByRole("radio", { name: "board.sort.directions.asc" }));
    expect(runtime.setSort).toHaveBeenCalledWith({ field: "created", direction: "asc" });
  });

  it("uses the primary custom summary", () => {
    runtime.sort = { field: "labels", direction: "asc" };
    render(<BoardSortChrome />);

    expect(screen.getByTestId("board-sort-trigger")).toHaveAttribute("data-selected", "true");
    expect(screen.getByTestId("board-sort-summary")).toHaveTextContent("board.sort.summary");
  });
});
