import type { ReactElement } from "react";
import { cloneElement } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import type {
  BoardFilterGenerationController,
  BoardFilterGenerationSnapshot,
} from "./BoardFilterGenerationController";

const labelRuntime = vi.hoisted(() => ({
  state: { filter: { kind: "named", mode: "all", labelIDs: ["label-1"] } },
  dispatch: vi.fn(),
}));
interface GenerationRuntime {
  snapshot: BoardFilterGenerationSnapshot;
  controller: Pick<BoardFilterGenerationController, "getSnapshot" | "setDesiredFilter">;
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

import { BoardFilterChrome } from "./BoardLabelFilter";

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
});
