import { fireEvent, render, screen } from "@testing-library/react";
import { vi } from "vitest";

import { dismissStatusToast, showStatusToast } from "../ui";
import type * as uiModule from "../ui";
import { StatusProvider } from "./statusStore";
import { useStatusController } from "./useStatusController";

vi.mock("../ui", async (importOriginal) => {
  const actual = await importOriginal<typeof uiModule>();
  return {
    ...actual,
    dismissStatusToast: vi.fn(),
    showStatusToast: vi.fn(),
    Toaster: () => null,
  };
});

describe("StatusProvider", () => {
  beforeEach(() => {
    vi.mocked(dismissStatusToast).mockClear();
    vi.mocked(showStatusToast).mockClear();
  });

  it("delegates pushed notices to the status toast adapter", () => {
    render(
      <StatusProvider>
        <TitleOnlyNoticeButton />
      </StatusProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Show" }));

    expect(showStatusToast).toHaveBeenCalledWith({
      id: "title-only",
      title: "Copied",
      tone: "success",
    });
  });

  it("delegates dismissals to the status toast adapter", () => {
    render(
      <StatusProvider>
        <DismissNoticeButton />
      </StatusProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));

    expect(dismissStatusToast).toHaveBeenCalledWith("title-only");
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
