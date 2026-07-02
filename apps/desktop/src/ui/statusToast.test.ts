import { toast } from "sonner";
import { vi } from "vitest";

import { showStatusToast } from "./statusToast";

vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), {
    custom: vi.fn(),
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

  it("uses one custom clickable toast body when the notice has a click handler", () => {
    const onClick = vi.fn();
    showStatusToast({
      body: "Open task detail",
      id: "attention-1",
      onClick,
      title: "Attention required",
      tone: "warning",
    });

    const customToastRenderer = vi.mocked(toast.custom).mock.calls[0]?.[0];
    expect(typeof customToastRenderer).toBe("function");
    expect(toast.custom).toHaveBeenCalledWith(customToastRenderer, {
      closeButton: false,
      id: "attention-1",
    });
    expect(toast.warning).not.toHaveBeenCalled();
  });
});
