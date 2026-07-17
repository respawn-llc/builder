import { fireEvent, render, screen } from "@testing-library/react";
import { toast } from "sonner";
import { vi } from "vitest";

import { StatusProvider } from "./statusStore";
import { useStatusController } from "./useStatusController";

vi.mock("sonner", () => ({
  Toaster: () => null,
  toast: Object.assign(vi.fn(), {
    dismiss: vi.fn(),
    error: vi.fn(),
    info: vi.fn(),
    success: vi.fn(),
    warning: vi.fn(),
  }),
}));

describe("StatusProvider", () => {
  beforeEach(() => {
    vi.mocked(toast.dismiss).mockClear();
    vi.mocked(toast.success).mockClear();
  });

  it("delegates pushed notices to the status toast adapter", () => {
    render(
      <StatusProvider>
        <TitleOnlyNoticeButton />
      </StatusProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Show" }));

    expect(toast.success).toHaveBeenCalledOnce();
  });

  it("delegates dismissals to the status toast adapter", () => {
    render(
      <StatusProvider>
        <DismissNoticeButton />
      </StatusProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));

    expect(toast.dismiss).toHaveBeenCalledWith("title-only");
  });
});

function TitleOnlyNoticeButton() {
  const { push } = useStatusController();
  return (
    <button
      onClick={() => {
        push({
          id: "title-only",
          title: "Copied",
          tone: "success",
        });
      }}
      type="button"
    >
      Show
    </button>
  );
}

function DismissNoticeButton() {
  const { dismiss } = useStatusController();
  return (
    <button
      onClick={() => {
        dismiss("title-only");
      }}
      type="button"
    >
      Dismiss
    </button>
  );
}
