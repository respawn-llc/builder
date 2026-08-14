import { render, screen } from "@testing-library/react";
import { cloneElement, type ReactElement, type ReactNode, type Ref } from "react";
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
const chooserTriggerAnchor = vi.hoisted<{ current: HTMLButtonElement | null }>(() => ({ current: null }));

vi.mock("@/shared/labels", () => ({
  LabelChooser: ({
    invocation,
    preferredSide,
    trigger,
  }: Readonly<{
    invocation: Readonly<{ disabled?: boolean }>;
    preferredSide?: "top" | "bottom";
    trigger: ReactElement<{ ref?: Ref<HTMLButtonElement> | undefined }>;
  }>) => (
    <div
      data-assignment-disabled={invocation.disabled === true ? "true" : "false"}
      data-preferred-side={preferredSide}
      data-testid="assignment-state"
    >
      {cloneElement(trigger, {
        ref(element) {
          chooserTriggerAnchor.current = element;
        },
      })}
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
    chooserTriggerAnchor.current = null;
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
    expect(screen.getByTestId("assignment-state")).toHaveAttribute("data-preferred-side", "top");
    expect(chooserTriggerAnchor.current).toBe(
      screen.getByRole("button", { name: "home.prototype.editTaskLabels" }),
    );
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
