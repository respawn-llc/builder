import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import {
  getCallCount,
  mountTaskDetailSurface,
  emptyTaskAttentionResponse,
  taskDetailResponse,
  taskUpdateResponse,
} from "@/test-support/task-detail";
import { appI18n } from "@/i18n";
import { createBrowserNativeBridge } from "@/test-support/native-bridge";
import { installResizeObserverGeometry } from "@/test-support/resize-observer";

describe("Task description checklist", () => {
  it("edits the local description draft and waits for Save before updating the Task", async () => {
    const services = mountTaskDetailSurface(
      {
        task: {
          ...taskDetailResponse.task,
          body: "Completion criteria:\n\n- [ ] Keep the Markdown source",
        },
      },
      {
        routes: [{ method: "workflow.task.update", result: taskUpdateResponse }],
      },
    );

    const checkbox = await screen.findByRole("checkbox");
    expect(checkbox).not.toBeChecked();
    expect(screen.queryByTestId("task-detail-save")).not.toBeInTheDocument();

    fireEvent.click(checkbox);

    expect(screen.getByRole("checkbox")).toBeChecked();
    expect(screen.getByTestId("task-detail-save")).toBeInTheDocument();
    expect(getCallCount(services.transport.calls, "workflow.task.update")).toBe(0);

    fireEvent.click(screen.getByTestId("task-detail-save"));

    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.update")).toBe(1);
    });
    expect(services.transport.calls).toContainEqual({
      method: "workflow.task.update",
      params: {
        task_id: "task-1",
        title: "Resolve blocker",
        body: "Completion criteria:\n\n- [x] Keep the Markdown source",
      },
    });
  });

  it.each(["Enter", " "])("keeps keyboard focus in read mode until %s requests editing", async (key) => {
    mountTaskDetailSurface(taskDetailResponse);
    const description = await screen.findByRole("textbox", { name: appI18n.t("task.description") });

    description.focus();

    expect(screen.getByRole("textbox", { name: appI18n.t("task.description") })).not.toBeInstanceOf(
      HTMLTextAreaElement,
    );

    fireEvent.keyDown(description, { key });

    expect(screen.getByRole("textbox", { name: appI18n.t("task.description") })).toBeInstanceOf(
      HTMLTextAreaElement,
    );
  });

  it("enters editing from plain pointer activation", async () => {
    mountTaskDetailSurface(taskDetailResponse);
    const user = userEvent.setup();
    const description = await screen.findByRole("textbox", { name: appI18n.t("task.description") });

    await user.click(description);

    expect(screen.getByRole("textbox", { name: appI18n.t("task.description") })).toBeInstanceOf(
      HTMLTextAreaElement,
    );
  });

  it("does not enter editing after a non-collapsed Markdown selection", async () => {
    mountTaskDetailSurface(taskDetailResponse);
    const description = await screen.findByRole("textbox", { name: appI18n.t("task.description") });
    const selection = window.getSelection();
    if (selection === null) throw new Error("Expected a browser selection.");

    selection.selectAllChildren(description);
    expect(selection.isCollapsed).toBe(false);

    fireEvent.pointerUp(description);
    fireEvent.click(description);

    expect(screen.getByRole("textbox", { name: appI18n.t("task.description") })).not.toBeInstanceOf(
      HTMLTextAreaElement,
    );
  });

  it("keeps safe links in read mode", async () => {
    mountTaskDetailSurface({
      task: {
        ...taskDetailResponse.task,
        body: "[Safe link](https://example.com)",
      },
    });
    const link = await screen.findByRole("button", { name: "Safe link" });

    fireEvent.click(link);

    expect(screen.getByRole("textbox", { name: appI18n.t("task.description") })).not.toBeInstanceOf(
      HTMLTextAreaElement,
    );
  });

  it("keeps task-list activation in read mode", async () => {
    mountTaskDetailSurface({
      task: {
        ...taskDetailResponse.task,
        body: "- [ ] Keep the Markdown source",
      },
    });
    const checkbox = await screen.findByRole("checkbox");

    fireEvent.click(checkbox);

    expect(screen.getByRole("textbox", { name: appI18n.t("task.description") })).not.toBeInstanceOf(
      HTMLTextAreaElement,
    );
  });

  it("returns to rendered Markdown on blur without losing the Draft", async () => {
    mountTaskDetailSurface(taskDetailResponse);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("textbox", { name: appI18n.t("task.description") }));
    const editor = screen.getByRole("textbox", { name: appI18n.t("task.description") });

    await user.clear(editor);
    await user.type(editor, "Unsaved body");
    fireEvent.blur(editor);

    expect(screen.getByText("Unsaved body")).toBeInTheDocument();
    expect(screen.getByTestId("task-detail-save")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: appI18n.t("task.description") })).not.toBeInstanceOf(
      HTMLTextAreaElement,
    );
  });

  it("returns to rendered Markdown when focus moves to the Task title", async () => {
    mountTaskDetailSurface(taskDetailResponse);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("textbox", { name: appI18n.t("task.description") }));
    const editor = screen.getByRole("textbox", { name: appI18n.t("task.description") });

    await user.clear(editor);
    await user.type(editor, "Blur manual QA draft");
    await user.click(screen.getByRole("textbox", { name: appI18n.t("task.name") }));

    await waitFor(() => {
      expect(screen.getByText("Blur manual QA draft")).toBeInTheDocument();
      expect(screen.getByRole("textbox", { name: appI18n.t("task.description") })).not.toBeInstanceOf(
        HTMLTextAreaElement,
      );
    });
    expect(screen.getByTestId("task-detail-save")).toBeInTheDocument();
  });

  it("does not edit or toggle a disabled description", async () => {
    const services = mountTaskDetailSurface({
      task: {
        ...taskDetailResponse.task,
        body: "- [ ] Keep the Markdown source",
      },
    });
    act(() => {
      services.transport.connection.set("disconnected", "offline");
    });
    const description = await screen.findByRole("textbox", { name: appI18n.t("task.description") });
    const checkbox = await screen.findByRole("checkbox");

    await waitFor(() => {
      expect(checkbox).toBeDisabled();
    });
    fireEvent.focus(description);
    fireEvent.keyDown(description, { key: "Enter" });
    fireEvent.click(checkbox);

    expect(screen.getByRole("textbox", { name: appI18n.t("task.description") })).not.toBeInstanceOf(
      HTMLTextAreaElement,
    );
    expect(checkbox).not.toBeChecked();
    expect(screen.queryByTestId("task-detail-save")).not.toBeInTheDocument();
  });

  it("closes clean editing from the native submit shortcut without updating the Task", async () => {
    const services = mountTaskDetailSurface(taskDetailResponse, {
      nativeBridge: createBrowserNativeBridge({ platform: "macos" }),
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("textbox", { name: appI18n.t("task.description") }));
    const editor = screen.getByRole("textbox", { name: appI18n.t("task.description") });

    fireEvent.keyDown(editor, { key: "Enter", metaKey: true });

    await waitFor(() => {
      expect(screen.getByRole("textbox", { name: appI18n.t("task.description") })).not.toBeInstanceOf(
        HTMLTextAreaElement,
      );
    });
    expect(getCallCount(services.transport.calls, "workflow.task.update")).toBe(0);
  });

  it("keeps dirty editing active until a deferred native shortcut Save resolves", async () => {
    const pending = deferred<typeof taskUpdateResponse>();
    const services = mountTaskDetailSurface(taskDetailResponse, {
      nativeBridge: createBrowserNativeBridge({ platform: "macos" }),
      routes: [
        {
          method: "workflow.task.update",
          handler: async () => pending.promise,
        },
      ],
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("textbox", { name: appI18n.t("task.description") }));
    const editor = screen.getByRole("textbox", { name: appI18n.t("task.description") });
    await user.clear(editor);
    await user.type(editor, "Unsaved body");

    fireEvent.keyDown(editor, { key: "Enter", metaKey: true });

    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.update")).toBe(1);
    });
    expect(screen.getByRole("textbox", { name: appI18n.t("task.description") })).toBeInstanceOf(
      HTMLTextAreaElement,
    );

    await act(async () => {
      pending.resolve(taskUpdateResponse);
    });
    await waitFor(() => {
      expect(screen.getByRole("textbox", { name: appI18n.t("task.description") })).not.toBeInstanceOf(
        HTMLTextAreaElement,
      );
    });
  });

  it("keeps the complete dirty Draft and editor active when a native shortcut Save rejects", async () => {
    const pending = deferred<typeof taskUpdateResponse>();
    const services = mountTaskDetailSurface(taskDetailResponse, {
      nativeBridge: createBrowserNativeBridge({ platform: "macos" }),
      routes: [
        {
          method: "workflow.task.update",
          handler: async () => pending.promise,
        },
      ],
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("textbox", { name: appI18n.t("task.description") }));
    const editor = screen.getByRole("textbox", { name: appI18n.t("task.description") });
    await user.clear(editor);
    await user.type(editor, "Unsaved body");

    fireEvent.keyDown(editor, { key: "Enter", metaKey: true });
    await waitFor(() => {
      expect(getCallCount(services.transport.calls, "workflow.task.update")).toBe(1);
    });
    await act(async () => {
      pending.reject(new Error("update failed"));
    });

    expect(await screen.findByText("update failed")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: appI18n.t("task.description") })).toBeInstanceOf(
      HTMLTextAreaElement,
    );
    expect(screen.getByRole("textbox", { name: appI18n.t("task.description") })).toHaveValue("Unsaved body");
  });

  it("does not offer Expand when the rendered content fits", async () => {
    const geometry = installResizeObserverGeometry();
    try {
      mountTaskDetailSurface(
        {
          task: {
            ...taskDetailResponse.task,
            body: "Short description",
          },
        },
        { attention: emptyTaskAttentionResponse },
      );
      await screen.findByRole("textbox", { name: appI18n.t("task.description") });
      const viewport = screen.getByTestId("markdown-field-read-content-viewport");
      geometry.setGeometry(viewport, { clientHeight: 100, scrollHeight: 100 });
      act(() => {
        geometry.notify();
      });

      expect(screen.queryByRole("button", { name: appI18n.t("app.expand") })).not.toBeInTheDocument();
    } finally {
      geometry.restore();
    }
  });

  it("offers Expand after controlled remeasurement detects overflow", async () => {
    const geometry = installResizeObserverGeometry();
    try {
      mountTaskDetailSurface(
        {
          task: {
            ...taskDetailResponse.task,
            body: "Long description",
          },
        },
        { attention: emptyTaskAttentionResponse },
      );
      await screen.findByRole("textbox", { name: appI18n.t("task.description") });
      const viewport = screen.getByTestId("markdown-field-read-content-viewport");
      geometry.setGeometry(viewport, { clientHeight: 100, scrollHeight: 100 });
      act(() => {
        geometry.notify();
      });
      expect(screen.queryByRole("button", { name: appI18n.t("app.expand") })).not.toBeInTheDocument();

      geometry.setGeometry(viewport, { clientHeight: 100, scrollHeight: 200 });
      act(() => {
        geometry.notify();
      });

      expect(await screen.findByRole("button", { name: appI18n.t("app.expand") })).toBeInTheDocument();
    } finally {
      geometry.restore();
    }
  });

  it("expands the description and keeps it expanded through later geometry notifications", async () => {
    const geometry = installResizeObserverGeometry();
    try {
      mountTaskDetailSurface(
        {
          task: {
            ...taskDetailResponse.task,
            body: "Long description",
          },
        },
        { attention: emptyTaskAttentionResponse },
      );
      await screen.findByRole("textbox", { name: appI18n.t("task.description") });
      const viewport = screen.getByTestId("markdown-field-read-content-viewport");
      geometry.setGeometry(viewport, { clientHeight: 100, scrollHeight: 200 });
      act(() => {
        geometry.notify();
      });
      await screen.findByRole("button", { name: appI18n.t("app.expand") });

      fireEvent.click(screen.getByRole("button", { name: appI18n.t("app.expand") }));
      await waitFor(() => {
        expect(screen.queryByRole("button", { name: appI18n.t("app.expand") })).not.toBeInTheDocument();
      });

      geometry.setGeometry(viewport, { clientHeight: 100, scrollHeight: 300 });
      act(() => {
        geometry.notify();
      });

      expect(screen.queryByRole("button", { name: appI18n.t("app.expand") })).not.toBeInTheDocument();
    } finally {
      geometry.restore();
    }
  });

  it("restores retained expanded presentation without offering Expand", async () => {
    const geometry = installResizeObserverGeometry();
    try {
      mountTaskDetailSurface(taskDetailResponse, {
        attention: emptyTaskAttentionResponse,
        retainedState: {
          base: { body: taskDetailResponse.task.body, title: taskDetailResponse.task.summary.title },
          descriptionPresentation: { editing: false, expanded: true },
          draft: { body: taskDetailResponse.task.body, title: taskDetailResponse.task.summary.title },
          editingComment: null,
          newCommentBody: "",
          scrollOffsetPx: 0,
          selectedTab: "comments",
        },
      });
      await screen.findByRole("textbox", { name: appI18n.t("task.description") });
      const viewport = screen.getByTestId("markdown-field-read-content-viewport");
      geometry.setGeometry(viewport, { clientHeight: 100, scrollHeight: 300 });
      act(() => {
        geometry.notify();
      });

      expect(screen.queryByRole("button", { name: appI18n.t("app.expand") })).not.toBeInTheDocument();
    } finally {
      geometry.restore();
    }
  });
});

function deferred<T>(): Readonly<{
  promise: Promise<T>;
  reject(error: unknown): void;
  resolve(value: T): void;
}> {
  let resolvePromise!: (value: T) => void;
  let rejectPromise!: (error: unknown) => void;
  const promise = new Promise<T>((resolve, reject) => {
    resolvePromise = resolve;
    rejectPromise = reject;
  });
  return {
    promise,
    reject: rejectPromise,
    resolve: resolvePromise,
  };
}
