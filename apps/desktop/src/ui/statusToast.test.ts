import { isValidElement } from "react";
import { toast } from "sonner";
import { vi } from "vitest";

import { showStatusToast } from "./statusToast";

vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), {
    dismiss: vi.fn(),
    error: vi.fn(),
    info: vi.fn(),
    success: vi.fn(),
    warning: vi.fn(),
  }),
}));

describe("showStatusToast", () => {
  it("does not pass a Sonner description for title-only notices", () => {
    showStatusToast({
      id: "title-only",
      title: "Copied",
      tone: "success",
    });

    expect(toast.success).toHaveBeenCalledWith("Copied", {
      closeButton: true,
      id: "title-only",
    });
  });

  it("does not pass a Sonner description for empty-body notices", () => {
    showStatusToast({
      body: "",
      id: "empty-body",
      title: "Copied",
      tone: "success",
    });

    expect(toast.success).toHaveBeenCalledWith("Copied", {
      closeButton: true,
      id: "empty-body",
    });
  });

  it("keeps persistent notices manually closeable", () => {
    showStatusToast({
      durationMs: Infinity,
      id: "persistent-error",
      title: "Task start failed",
      tone: "danger",
    });

    expect(toast.error).toHaveBeenCalledWith("Task start failed", {
      closeButton: true,
      duration: Infinity,
      id: "persistent-error",
    });
  });

  it("keeps clickable notices on Sonner styled toasts", () => {
    const onClick = vi.fn();
    showStatusToast({
      body: "Open task detail",
      id: "attention-1",
      onClick,
      title: "Attention required",
      tone: "warning",
    });

    expect(toast.warning).toHaveBeenCalledOnce();
    const title: unknown = vi.mocked(toast.warning).mock.calls[0]?.[0];
    expect(isValidElement<{ type?: string }>(title)).toBe(true);
    if (!isValidElement<{ type?: string }>(title)) {
      throw new Error("Expected clickable toast title element.");
    }
    expect(title.type).toBe("button");
    expect(title.props.type).toBe("button");
    expect(vi.mocked(toast.warning).mock.calls[0]?.[1]).toEqual({
      closeButton: true,
      id: "attention-1",
    });
  });

  it("does not render a separate Sonner action for clickable notices", () => {
    showStatusToast({
      actionLabel: "Open",
      body: "Open task detail",
      id: "attention-1",
      onAction: vi.fn(),
      onClick: vi.fn(),
      title: "Attention required",
      tone: "warning",
    });

    expect(vi.mocked(toast.warning).mock.calls[0]?.[1]).toEqual({
      closeButton: true,
      id: "attention-1",
    });
  });
});
