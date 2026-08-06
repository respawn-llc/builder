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

type LabelRuntimeState =
  | Readonly<{ filter: { kind: "none" } }>
  | Readonly<{ filter: { kind: "named"; mode: "all"; labelIDs: string[] } }>;

const labelRuntime = vi.hoisted(
  (): {
    state: LabelRuntimeState;
    dispatch: ReturnType<typeof vi.fn>;
  } => ({
    state: {
      filter: { kind: "named", mode: "all", labelIDs: ["label-1"] },
    },
    dispatch: vi.fn(),
  }),
);
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
    t: (key: string) => key,
  }),
}));

vi.mock("./BoardFilterGenerationRuntime", () => ({
  useBoardFilterGeneration: () => generationRuntime,
}));

vi.mock("./TaskSearchChrome", () => ({
  TaskSearchProjectTrigger: () => <button type="button" />,
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

    const chip = screen.getAllByRole("button").at(-1);
    if (chip === undefined) {
      throw new Error("Expected the dependency filter control.");
    }
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
    expect(screen.getAllByRole("button").at(-1)).toHaveAttribute("aria-pressed", "true");
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

    const labelTrigger = screen.getAllByRole("button").at(0);
    if (labelTrigger === undefined) {
      throw new Error("Expected the label filter control.");
    }
    await user.click(labelTrigger);

    expect(generationRuntime.controller.setDesiredFilter).toHaveBeenCalledWith({
      labelFilter: { kind: "none" },
      dependencyFilter: true,
    });
  });

  it("keeps Labels, Sort, Unblocked, and Search in the board chrome order", () => {
    labelRuntime.state = { filter: { kind: "none" } };
    generationRuntime.snapshot = {
      active: {
        generation: 1,
        filter: {
          labelFilter: { kind: "none" },
          dependencyFilter: true,
        },
        retiring: false,
      },
      desiredFilter: null,
    };
    render(<BoardFilterRow onOpenTask={vi.fn()} projectID="project-1" />);

    const controls = screen.getAllByRole("button");
    expect(controls).toHaveLength(4);
    const labels = controlAt(controls, 0);
    const sort = controlAt(controls, 1);
    const unblocked = controlAt(controls, 2);
    const search = controlAt(controls, 3);
    expect(labels.compareDocumentPosition(sort) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(sort.compareDocumentPosition(unblocked) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
    expect(unblocked.compareDocumentPosition(search) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0);
  });
});

function controlAt(controls: readonly HTMLElement[], index: number): HTMLElement {
  const control = controls[index];
  if (control === undefined) {
    throw new Error(`Expected board chrome control at index ${String(index)}.`);
  }
  return control;
}
