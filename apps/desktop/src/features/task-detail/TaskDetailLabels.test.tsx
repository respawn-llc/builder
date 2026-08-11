import { fireEvent, render, screen } from "@testing-library/react";
import type { ButtonHTMLAttributes, ReactElement, ReactNode } from "react";
import { vi } from "vitest";

import { TaskDetailLabels } from "./TaskDetailLabels";

const ids = vi.hoisted(() => ({
  alpha: "38bf0da7-a3f7-4c15-bc5f-c8fca538e667",
}));
interface AssignmentFixture {
  error: Error | null;
  failures: Readonly<
    Readonly<{
      desiredSelected: boolean;
      error: unknown;
      labelID: string;
    }>
  >[];
  isPending: boolean;
  pendingLabelIDs: string[];
  retry: ReturnType<typeof vi.fn>;
  retryLoad: ReturnType<typeof vi.fn>;
  selectedLabelIDs: string[];
  setSelected: ReturnType<typeof vi.fn>;
}
const assignment = vi.hoisted<AssignmentFixture>(() => ({
  error: null,
  failures: [],
  isPending: false,
  pendingLabelIDs: [],
  retry: vi.fn(),
  retryLoad: vi.fn(),
  selectedLabelIDs: [ids.alpha],
  setSelected: vi.fn(),
}));

interface LabelChooserInvocation {
  onSelectionChange(labelID: string, selected: boolean): void;
}

vi.mock("@/shared/labels", () => ({
  LabelChooser: ({
    invocation,
    trigger,
  }: Readonly<{
    invocation: LabelChooserInvocation;
    trigger: ReactElement;
  }>) => (
    <div>
      {trigger}
      <button
        onClick={() => {
          invocation.onSelectionChange(ids.alpha, false);
        }}
        type="button"
      >
        Change assignment
      </button>
    </div>
  ),
  orderedAssignedLabels: (
    catalog: Readonly<{ labels: readonly Readonly<{ id: string; name: string }>[] }>,
    selectedLabelIDs: readonly string[],
  ) => catalog.labels.filter((label) => selectedLabelIDs.includes(label.id)),
  useProjectLabelCatalog: () => ({
    data: {
      projectID: "project-1",
      labels: [{ id: ids.alpha, name: "Alpha" }],
    },
    isPending: false,
  }),
  useTaskLabelAssignment: () => assignment,
}));

vi.mock("./TaskPropertyLine", () => ({
  TaskPropertyLine: ({ value }: Readonly<{ value: ReactElement }>) => value,
}));

vi.mock("@/ui", () => ({
  Badge: ({ children }: Readonly<{ children: ReactNode }>) => <>{children}</>,
  Button: ({ children, ...props }: Readonly<ButtonHTMLAttributes<HTMLButtonElement>>) => (
    <button {...props}>{children}</button>
  ),
  Spinner: () => <span role="status">Loading</span>,
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}));

describe("TaskDetailLabels", () => {
  beforeEach(() => {
    assignment.error = null;
    assignment.failures = [];
    assignment.isPending = false;
    assignment.pendingLabelIDs = [];
    assignment.retry.mockClear();
    assignment.retryLoad.mockClear();
    assignment.selectedLabelIDs = [ids.alpha];
    assignment.setSelected.mockClear();
  });

  it("shows assignment loading and query Retry without enabling the chooser", () => {
    assignment.isPending = true;
    const view = render(<TaskDetailLabels disabled={false} />);

    expect(screen.getByRole("status")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "labels.editAssignments" })).toBeDisabled();

    assignment.isPending = false;
    assignment.error = new Error("assignment unavailable");
    view.rerender(<TaskDetailLabels disabled={false} />);
    fireEvent.click(screen.getByRole("button", { name: "app.retry" }));

    expect(screen.getByText("assignment unavailable")).toBeInTheDocument();
    expect(assignment.retryLoad).toHaveBeenCalledOnce();
  });

  it("renders pending chips and sends chooser changes through the direct command", () => {
    assignment.pendingLabelIDs = [ids.alpha];
    render(<TaskDetailLabels disabled={false} />);

    expect(screen.getByText("Alpha")).toHaveClass("opacity-60");
    fireEvent.click(screen.getByRole("button", { name: "Change assignment" }));
    expect(assignment.setSelected).toHaveBeenCalledWith(ids.alpha, false);
  });

  it("keeps per-Label mutation failures visible with Retry", () => {
    assignment.failures = [
      {
        desiredSelected: true,
        error: new Error("save failed"),
        labelID: ids.alpha,
      },
    ];
    render(<TaskDetailLabels disabled={false} />);

    expect(screen.getByText("save failed")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "app.retry" }));
    expect(assignment.retry).toHaveBeenCalledWith(ids.alpha);
  });
});
