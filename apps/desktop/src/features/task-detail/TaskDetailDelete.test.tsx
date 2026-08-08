import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

import { RpcError, rpcErrorCodes } from "@/api";
import { getCallCount, mountTaskDetailSurface, taskDetailResponse } from "@/test-support/task-detail";

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

  expect(await screen.findByRole("button", { name: "Delete" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();

  const title = screen.getByRole("textbox", { name: "Title" });
  await user.type(title, " changed");

  expect(screen.getByRole("button", { name: "Save changes" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
});

it("leaves the clean title action slot empty when Delete is unavailable", async () => {
  mountTaskDetailSurface(taskWithDelete(false));

  await screen.findByRole("textbox", { name: "Title" });
  expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
});

it("dismisses its exact host after successful deletion", async () => {
  const onDeleteDismiss = vi.fn(async () => ({ kind: "accepted" as const }));
  const services = mountTaskDetailSurface(taskWithDelete(true), {
    onDeleteDismiss,
    routes: [{ method: "workflow.task.delete", result: {} }],
  });
  const user = userEvent.setup();

  await user.click(await screen.findByRole("button", { name: "Delete" }));
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete" }));

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

  await user.click(await screen.findByRole("button", { name: "Delete" }));
  const confirm = within(screen.getByRole("dialog")).getByRole("button", { name: "Delete" });
  await user.click(confirm);
  expect(await screen.findByText("temporary failure")).toBeInTheDocument();
  expect(onDeleteDismiss).not.toHaveBeenCalled();

  await user.click(confirm);
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

  await user.click(await screen.findByRole("button", { name: "Delete" }));
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete" }));

  expect(await screen.findByText("dismiss failed")).toBeInTheDocument();
  expect(onDeleteDismiss).toHaveBeenCalledOnce();
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

  await user.click(await screen.findByRole("button", { name: "Delete" }));
  await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Delete" }));
  await waitFor(() => {
    expect(getCallCount(services.transport.calls, "workflow.task.delete")).toBe(1);
  });

  services.unmountTaskDetail();
  resolveDelete?.();

  await waitFor(() => {
    expect(onDeleteDismiss).toHaveBeenCalledOnce();
  });
});
