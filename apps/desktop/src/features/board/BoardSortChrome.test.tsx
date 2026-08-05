import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

type SortState = Readonly<{
  field: "created" | "updated" | "labels" | "short_id";
  direction: "asc" | "desc";
}>;

const runtime = vi.hoisted((): { sort: SortState; setSort: ReturnType<typeof vi.fn> } => ({
  sort: { field: "updated", direction: "desc" },
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

    const trigger = screen.getByRole("button");
    expect(trigger).toHaveAttribute("data-selected", "false");
    await user.click(trigger);

    const content = screen.getByRole("dialog");
    const radios = within(content).getAllByRole("radio");
    expect(radios.slice(0, boardSortFieldOptions.length).map((radio) => radio.getAttribute("value"))).toEqual(
      boardSortFieldOptions.map((option) => option.value),
    );
    expect(radios.slice(boardSortFieldOptions.length).map((radio) => radio.getAttribute("value"))).toEqual([
      "asc",
      "desc",
    ]);
    expect(runtime.setSort).not.toHaveBeenCalled();
  });

  it("applies field and direction changes immediately while retaining the popover", async () => {
    const user = userEvent.setup();
    const view = render(<BoardSortChrome />);
    await user.click(screen.getByRole("button"));

    const content = screen.getByRole("dialog");
    const findRadio = (value: string): HTMLElement => {
      const radio = within(content)
        .getAllByRole("radio")
        .find((candidate) => candidate.getAttribute("value") === value);
      if (radio === undefined) {
        throw new Error(`Expected sort radio "${value}".`);
      }
      return radio;
    };
    await user.click(findRadio("created"));
    expect(runtime.setSort).toHaveBeenCalledWith({ field: "created", direction: "desc" });
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    runtime.sort = { field: "created", direction: "desc" };
    view.rerender(<BoardSortChrome />);
    await user.click(findRadio("asc"));
    expect(runtime.setSort).toHaveBeenCalledWith({ field: "created", direction: "asc" });
  });

  it("uses the primary custom summary", () => {
    runtime.sort = { field: "labels", direction: "asc" };
    render(<BoardSortChrome />);

    expect(screen.getByRole("button")).toHaveAttribute("data-selected", "true");
  });
});
