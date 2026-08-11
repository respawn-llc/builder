import { render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";

import { RpcError, rpcErrorCodes } from "@/api";
import { createTestSidebarNavigator } from "@/test-support/sidebar";
import { TaskDetailSurface } from "./TaskDetailSurface";

const contentProps = vi.hoisted(() => vi.fn<(props: unknown) => void>());
const fixture = vi.hoisted<{ detail: unknown }>(() => ({
  detail: { isPending: true },
}));

vi.mock("./useTaskDetailData", () => ({
  useTaskActivity: () => ({ data: undefined }),
  useTaskAttention: () => ({ data: undefined, isError: false }),
  useTaskComments: () => ({ data: undefined }),
  useTaskDetail: () => fixture.detail,
}));

vi.mock("./TaskDetailContent", () => ({
  TaskDetailContent: (props: unknown) => {
    contentProps(props);
    return <div data-testid="task-detail-content" />;
  },
}));

vi.mock("@/shared/labels", () => ({
  ProjectLabelsProvider: ({ children }: Readonly<{ children: ReactNode }>) => <>{children}</>,
  TaskLabelAssignmentProvider: ({ children }: Readonly<{ children: ReactNode }>) => <>{children}</>,
}));

vi.mock("@/app-facade", async (importOriginal) => ({
  ...(await importOriginal()),
  useStatusController: () => ({ push: vi.fn() }),
}));

vi.mock("@/ui", () => ({
  ErrorState: ({ body }: Readonly<{ body: string }>) => <div data-testid="error-state">{body}</div>,
  LoadingState: () => <div data-testid="loading-state" />,
}));

describe("TaskDetailSurface sidebar ownership", () => {
  beforeEach(() => {
    contentProps.mockClear();
    fixture.detail = { isPending: true };
  });

  it("dismisses a typed missing Task before mounting content or capture ownership", async () => {
    const page = createTestSidebarNavigator();
    fixture.detail = {
      error: new RpcError({
        code: rpcErrorCodes.workflowTaskNotFound,
        message: "gone",
        method: "workflow.task.get",
      }),
      isError: true,
      isPending: false,
    };

    render(<TaskDetailSurface enabled navigator={page} taskId="task-1" />);

    await waitFor(() => {
      expect(page.back).toHaveBeenCalledOnce();
    });
    expect(contentProps).not.toHaveBeenCalled();
    expect(page.registerCapture).not.toHaveBeenCalled();
  });

  it("keeps ordinary failures in the existing recovery surface", () => {
    const page = createTestSidebarNavigator();
    fixture.detail = {
      error: new Error("offline"),
      isError: true,
      isPending: false,
    };

    render(<TaskDetailSurface enabled navigator={page} taskId="task-1" />);

    expect(screen.getByTestId("error-state")).toBeInTheDocument();
    expect(page.back).not.toHaveBeenCalled();
  });

  it("forwards retained state and first-open focus only after fresh Task data loads", () => {
    const retainedState = { draft: { title: "Draft", body: "Body" } };
    const initialFocus = { kind: "dependencies" } as const;
    fixture.detail = {
      data: {
        id: "task-1",
        labelIDs: [],
        projectID: "project-1",
        workflowID: "workflow-1",
      },
      isError: false,
      isPending: false,
    };

    render(
      <TaskDetailSurface
        enabled
        initialFocus={initialFocus}
        navigator={createTestSidebarNavigator()}
        retainedState={retainedState}
        taskId="task-1"
      />,
    );

    expect(screen.getByTestId("task-detail-content")).toBeInTheDocument();
    expect(contentProps).toHaveBeenCalledWith(expect.objectContaining({ initialFocus, retainedState }));
  });
});
