import type { ReactElement } from "react";
import { cloneElement } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import type {
  BoardFilterGenerationController,
  BoardFilterGenerationSnapshot,
} from "./BoardFilterGenerationController";
import type { BoardNodeCardsSort } from "@/api";

const labelRuntime = vi.hoisted(() => ({
  state: { filter: { kind: "named", mode: "all", labelIDs: ["label-1"] } },
  dispatch: vi.fn(),
}));
interface GenerationRuntime {
  snapshot: BoardFilterGenerationSnapshot;
  controller: Pick<BoardFilterGenerationController, "getSnapshot" | "setDesiredFilter">;
  sort: BoardNodeCardsSort;
  setSort(sort: BoardNodeCardsSort): void;
}
const generationRuntime = vi.hoisted((): GenerationRuntime => ({
  snapshot: {
    active: {
      generation: 1,
      filter: {
        labelFilter: {
          kind: "named",
          mode: "all",
          labelIDs: ["label-1"],
          excludedLabelIDs: [],
        },
        dependencyFilter: true,
      },
      retiring: false,
    },
    desiredFilter: null,
  },
  controller: {
    getSnapshot: () => generationRuntime.snapshot,
    setDesiredFilter: vi.fn(),
  },
  sort: { field: "updated", direction: "desc" },
  setSort: vi.fn(),
}));

vi.mock("@/shared/labels", () => ({
  LabelChooser: ({
    invocation,
    trigger,
  }: {
    invocation: { onAction(action: unknown): void };
    trigger: ReactElement<{ onClick?: () => void }>;
  }) =>
    cloneElement(trigger, {
      onClick: () => {
        invocation.onAction({ type: "clear" });
      },
    }),
  reduceLabelFilterState: () => ({ filter: { kind: "none" } }),
  taskLabelFilterConditionCount: () => 1,
  useProjectLabelFilter: () => labelRuntime,
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string) => {
      if (key === "board.unblocked") {
        return "dependency-control";
      }
      if (key === "labels.filterCount") {
        return "label-control";
      }
      return key;
    },
  }),
}));

vi.mock("./BoardFilterGenerationRuntime", () => ({
  useBoardFilterGeneration: () => generationRuntime,
}));

vi.mock("./BoardTaskSearch", () => ({
  BoardTaskSearchChrome: () => <button type="button">Search</button>,
}));

import { BoardFilterChrome } from "./BoardLabelFilter";
import { BoardFilterRow } from "./BoardFilterRow";

describe("BoardFilterChrome", () => {
  it("toggles Unblocked through the latest desired composite filter", async () => {
    const user = userEvent.setup();
    generationRuntime.snapshot = {
      active: {
        generation: 1,
        filter: {
          labelFilter: { kind: "named", mode: "all", labelIDs: ["label-1"], excludedLabelIDs: [] },
          dependencyFilter: null,
        },
        retiring: false,
      },
      desiredFilter: null,
    };
    const view = render(<BoardFilterChrome />);

    const chip = screen.getByRole("button", { name: "dependency-control" });
    expect(chip).toHaveAttribute("aria-pressed", "false");
    await user.click(chip);

    expect(generationRuntime.controller.setDesiredFilter).toHaveBeenCalledWith({
      labelFilter: { kind: "named", mode: "all", labelIDs: ["label-1"], excludedLabelIDs: [] },
      dependencyFilter: true,
    });

    generationRuntime.snapshot = {
      ...generationRuntime.snapshot,
      active: {
        ...generationRuntime.snapshot.active,
        filter: { ...generationRuntime.snapshot.active.filter, dependencyFilter: true },
      },
    };
    view.rerender(<BoardFilterChrome />);
    expect(screen.getByRole("button", { name: "dependency-control" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("preserves the dependency filter when the Labels filter changes", async () => {
    const user = userEvent.setup();
    generationRuntime.snapshot = {
      active: {
        generation: 1,
        filter: {
          labelFilter: { kind: "named", mode: "all", labelIDs: ["label-1"], excludedLabelIDs: [] },
          dependencyFilter: true,
        },
        retiring: false,
      },
      desiredFilter: null,
    };
    render(<BoardFilterChrome />);

    await user.click(screen.getByRole("button", { name: "label-control" }));

    expect(generationRuntime.controller.setDesiredFilter).toHaveBeenCalledWith({
      labelFilter: { kind: "none" },
      dependencyFilter: true,
    });
  });

  it("keeps Labels, Sort, Unblocked, and Search in the board chrome order", () => {
    labelRuntime.state = { filter: { kind: "named", mode: "all", labelIDs: ["label-1"] } };
    generationRuntime.snapshot = {
      active: {
        generation: 1,
        filter: {
          labelFilter: { kind: "named", mode: "all", labelIDs: ["label-1"], excludedLabelIDs: [] },
          dependencyFilter: true,
        },
        retiring: false,
      },
      desiredFilter: null,
    };
    render(<BoardFilterRow onOpenTask={vi.fn()} projectID="project-1" />);

    const labels = screen.getByRole("button", { name: "label-control" });
    const sort = screen.getByRole("button", { name: "board.sort.chip" });
    const unblocked = screen.getByRole("button", { name: "dependency-control" });
    const search = screen.getByRole("button", { name: "Search" });
    expect(labels.compareDocumentPosition(sort) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(sort.compareDocumentPosition(unblocked) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(unblocked.compareDocumentPosition(search) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  });
});
