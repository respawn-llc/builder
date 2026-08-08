import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import { RpcError, rpcErrorCodes } from "@/api";
import { appI18n } from "@/i18n";
import { getCallCount, mountTaskDetailSurface, taskDetailResponse } from "@/test-support/task-detail";

const statusError = vi.hoisted(() => vi.fn(() => "status-error"));
vi.mock("sonner", () => {
  const toast = Object.assign(
    vi.fn(() => "status"),
    {
      dismiss: vi.fn(),
      error: statusError,
      info: vi.fn(() => "status-info"),
      success: vi.fn(() => "status-success"),
      warning: vi.fn(() => "status-warning"),
    },
  );
  return { toast, Toaster: () => null };
});

function taskWithDelete(canDelete: boolean) {
  return {
    task: {
      ...taskDetailResponse.task,
      attention_count: 0,
      actions: {
        ...taskDetailResponse.task.actions,
        can_delete: canDelete,
      },
    },
  };
}

it("shows Delete only for a clean deletable Task and replaces it with Save while dirty", async () => {
  mountTaskDetailSurface(taskWithDelete(true));
  const user = userEvent.setup();
  const deleteLabel = appI18n.t("board.deleteTask");
  const saveLabel = appI18n.t("task.save");

  expect(await screen.findByRole("button", { name: deleteLabel })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: saveLabel })).not.toBeInTheDocument();

  const title = screen.getByRole("textbox", { name: appI18n.t("task.name") });
  await user.type(title, " changed");

  expect(screen.getByRole("button", { name: saveLabel })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: deleteLabel })).not.toBeInTheDocument();
  const inactiveDelete = screen.getByTestId("task-detail-delete");
  expect(inactiveDelete).toBeDisabled();
  expect(inactiveDelete).toHaveAttribute("aria-hidden", "true");
  expect(inactiveDelete).toHaveAttribute("tabindex", "-1");
});

it("leaves the clean title action slot empty when Delete is unavailable", async () => {
  mountTaskDetailSurface(taskWithDelete(false));

  await screen.findByRole("textbox", { name: appI18n.t("task.name") });
  expect(screen.queryByRole("button", { name: appI18n.t("board.deleteTask") })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: appI18n.t("task.save") })).not.toBeInTheDocument();
});

it("dismisses its exact host after successful deletion", async () => {
  const onDeleteDismiss = vi.fn(async () => ({ kind: "accepted" as const }));
  const services = mountTaskDetailSurface(taskWithDelete(true), {
    onDeleteDismiss,
    routes: [
      {
        method: "workflow.task.delete",
        handler: async () => {
          expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
          return {};
        },
      },
    ],
  });
  const user = userEvent.setup();
  const deleteLabel = appI18n.t("board.deleteTask");

  await user.click(await screen.findByRole("button", { name: deleteLabel }));
  await user.click(
    within(screen.getByRole("dialog")).getByRole("button", {
      name: appI18n.t("board.deleteTaskConfirm"),
    }),
  );

  await waitFor(() => {
    expect(getCallCount(services.transport.calls, "workflow.task.delete")).toBe(1);
    expect(onDeleteDismiss).toHaveBeenCalledOnce();
  });
});

it("treats typed Task-not-found as deletion and lets the user retry an ordinary request failure", async () => {
  let attempts = 0;
  const onDeleteDismiss = vi.fn(async () => ({ kind: "accepted" as const }));
  const services = mountTaskDetailSurface(taskWithDelete(true), {
    onDeleteDismiss,
    routes: [
      {
        method: "workflow.task.delete",
        handler: async () => {
          attempts += 1;
          if (attempts === 1) {
            throw new Error("temporary failure");
          }
          throw new RpcError({
            code: rpcErrorCodes.workflowTaskNotFound,
            message: "Task not found",
            method: "workflow.task.delete",
          });
        },
      },
    ],
  });
  const user = userEvent.setup();
  const deleteLabel = appI18n.t("board.deleteTask");
  const confirmLabel = appI18n.t("board.deleteTaskConfirm");

  await user.click(await screen.findByRole("button", { name: deleteLabel }));
  const confirm = within(screen.getByRole("dialog")).getByRole("button", { name: confirmLabel });
  await user.click(confirm);
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  await waitFor(() => {
    expect(getCallCount(services.transport.calls, "workflow.task.delete")).toBe(1);
    expect(screen.getByRole("button", { name: deleteLabel })).not.toBeDisabled();
    expect(onDeleteDismiss).not.toHaveBeenCalled();
  });

  await user.click(screen.getByRole("button", { name: deleteLabel }));
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: confirmLabel }));
  await waitFor(() => {
    expect(getCallCount(services.transport.calls, "workflow.task.delete")).toBe(2);
    expect(onDeleteDismiss).toHaveBeenCalledOnce();
  });
});

it("surfaces an exact-host dismissal failure without closing a replacement", async () => {
  const onDeleteDismiss = vi.fn(async () => ({
    kind: "failed" as const,
    error: new Error("dismiss failed"),
  }));
  mountTaskDetailSurface(taskWithDelete(true), {
    onDeleteDismiss,
    routes: [{ method: "workflow.task.delete", result: {} }],
  });
  const user = userEvent.setup();
  const deleteLabel = appI18n.t("board.deleteTask");

  await user.click(await screen.findByRole("button", { name: deleteLabel }));
  await user.click(
    within(screen.getByRole("dialog")).getByRole("button", {
      name: appI18n.t("board.deleteTaskConfirm"),
    }),
  );
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

  await waitFor(() => {
    expect(onDeleteDismiss).toHaveBeenCalledOnce();
    expect(screen.getByRole("button", { name: deleteLabel })).not.toBeDisabled();
    expect(statusError).toHaveBeenLastCalledWith(
      expect.anything(),
      expect.objectContaining({ id: "task-detail-delete-dismiss-error" }),
    );
  });
});

it("still best-effort dismisses its exact host when ordinary Task Detail content is replaced in flight", async () => {
  let resolveDelete: (() => void) | undefined;
  const onDeleteDismiss = vi.fn(async () => ({ kind: "accepted" as const }));
  const services = mountTaskDetailSurface(taskWithDelete(true), {
    onDeleteDismiss,
    routes: [
      {
        method: "workflow.task.delete",
        handler: async () =>
          new Promise<Record<string, never>>((resolve) => {
            resolveDelete = () => {
              resolve({});
            };
          }),
      },
    ],
  });
  const user = userEvent.setup();
  const deleteLabel = appI18n.t("board.deleteTask");

  await user.click(await screen.findByRole("button", { name: deleteLabel }));
  await user.click(
    within(screen.getByRole("dialog")).getByRole("button", {
      name: appI18n.t("board.deleteTaskConfirm"),
    }),
  );
  await waitFor(() => {
    expect(getCallCount(services.transport.calls, "workflow.task.delete")).toBe(1);
  });

  services.unmountTaskDetail();
  resolveDelete?.();

  await waitFor(() => {
    expect(onDeleteDismiss).toHaveBeenCalledOnce();
  });
});
