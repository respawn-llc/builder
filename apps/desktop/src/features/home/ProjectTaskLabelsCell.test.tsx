import { render, screen } from "@testing-library/react";
import type { ReactElement, ReactNode } from "react";
import { useTranslation } from "react-i18next";

import type { TaskLabelAssignmentData } from "@/shared/labels";
import { ProjectTaskLabelsCell } from "./ProjectTaskLabelsCell";

type MutableAssignment = {
  -readonly [Key in keyof TaskLabelAssignmentData]: TaskLabelAssignmentData[Key];
};

const assignment = vi.hoisted<MutableAssignment>(() => ({
  error: null,
  failures: [],
  isPending: false,
  pendingLabelIDs: [],
  retry: vi.fn(),
  retryLoad: vi.fn(),
  selectedLabelIDs: [],
  setSelected: vi.fn(),
}));

vi.mock("@/shared/labels", () => ({
  LabelChooser: ({
    invocation,
    trigger,
  }: Readonly<{
    invocation: Readonly<{ disabled?: boolean }>;
    trigger: ReactElement;
  }>) => (
    <div
      data-assignment-disabled={invocation.disabled === true ? "true" : "false"}
      data-testid="assignment-state"
    >
      {trigger}
    </div>
  ),
  ProjectLabelsProvider: ({ children }: Readonly<{ children: ReactNode }>): ReactNode => children,
  TaskLabelAssignmentFeedback: () => null,
  TaskLabelAssignmentProvider: ({ children }: Readonly<{ children: ReactNode }>): ReactNode => children,
  useTaskLabelAssignment: () => assignment,
}));

describe("ProjectTaskLabelsCell", () => {
  beforeEach(() => {
    assignment.error = null;
    assignment.isPending = false;
  });

  it("reuses assignment loading and error gating after the lazy row editor mounts", () => {
    assignment.isPending = true;
    const view = renderCell();

    expect(screen.getByRole("button", { name: "home.prototype.editTaskLabels" })).toBeDisabled();
    expect(screen.getByTestId("spinner")).toBeInTheDocument();
    expect(screen.getByTestId("assignment-state")).toHaveAttribute("data-assignment-disabled", "true");

    assignment.isPending = false;
    assignment.error = new Error("assignment unavailable");
    view.rerender(cell());

    expect(screen.getByRole("button", { name: "home.prototype.editTaskLabels" })).toBeDisabled();
    expect(screen.queryByTestId("spinner")).not.toBeInTheDocument();
    expect(screen.getByText("Priority")).toBeInTheDocument();
    expect(screen.getByTestId("assignment-state")).toHaveAttribute("data-assignment-disabled", "true");
  });
});

function renderCell() {
  return render(cell());
}

function cell() {
  return <TestCell />;
}

function TestCell() {
  const { t } = useTranslation();
  return (
    <ProjectTaskLabelsCell
        onOpenChange={vi.fn()}
        open
        projectID="project-1"
        t={t}
        task={{
          id: "task-1",
          shortID: "KNT-1",
          workflowID: "workflow-1",
          workflowName: "Delivery",
          title: "Task",
          createdAt: 0,
          updatedAt: 0,
          columnKeys: null,
          status: {
            attentionTypes: [],
            kind: "backlog",
            nativeState: "backlog",
            nodeIDs: [],
          },
          labels: [{ id: "label-1", name: "Priority" }],
          dependencyProgress: null,
        }}
    />
  );
}
