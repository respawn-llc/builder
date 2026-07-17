import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, vi } from "vitest";

import { TooltipProvider } from "./radix/tooltip";
import { DisabledInteractionGuard } from "./DisabledInteractionGuard";

describe("DisabledInteractionGuard", () => {
  const originalResizeObserver = globalThis.ResizeObserver;

  beforeEach(() => {
    globalThis.ResizeObserver = MockResizeObserver;
  });

  afterEach(() => {
    globalThis.ResizeObserver = originalResizeObserver;
  });

  it("shows one tooltip announcement and blocks pointer and keyboard activation", async () => {
    const user = userEvent.setup();
    const onClick = vi.fn();
    const onKeyDown = vi.fn();
    const onPointerDown = vi.fn();

    render(
      <TooltipProvider delayDuration={0}>
        <DisabledInteractionGuard disabled reason="Unavailable for this edge">
          <button onClick={onClick} onKeyDown={onKeyDown} onPointerDown={onPointerDown} type="button">
            Guarded action
          </button>
        </DisabledInteractionGuard>
      </TooltipProvider>,
    );

    const guardedAction = screen.getByRole("button", { name: "Guarded action" });
    fireEvent.pointerDown(guardedAction);
    fireEvent.click(guardedAction);
    fireEvent.keyDown(guardedAction, { code: "Enter", key: "Enter" });
    fireEvent.keyDown(guardedAction, { code: "Space", key: " " });

    expect(onClick).not.toHaveBeenCalled();
    expect(onKeyDown).not.toHaveBeenCalled();
    expect(onPointerDown).not.toHaveBeenCalled();

    await user.hover(guardedAction);

    await screen.findByRole("tooltip");
    expect(screen.getAllByRole("tooltip")).toHaveLength(1);
    expect(screen.queryByTestId("tooltip-arrow")).not.toBeInTheDocument();
  });
});

class MockResizeObserver implements ResizeObserver {
  observe(): void {
    return;
  }

  unobserve(): void {
    return;
  }

  disconnect(): void {
    return;
  }
}
