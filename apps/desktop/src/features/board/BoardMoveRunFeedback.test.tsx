import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useBoardMoveRunFeedback } from "./BoardMoveRunFeedback";

const { push } = vi.hoisted(() => ({ push: vi.fn() }));

vi.mock("../../app/useStatusController", () => ({
  useStatusController: () => ({ push }),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

function Harness() {
  const tracker = useBoardMoveRunFeedback();
  return (
    <div>
      <button
        onClick={() => {
          tracker.trackMoveRunIDs({ runIDs: ["run-1"] });
        }}
        type="button"
      >
        track
      </button>
      <button
        onClick={() => {
          tracker.observeInterruptedRun({ runID: "run-1", taskID: "task-1" });
        }}
        type="button"
      >
        observe-tracked
      </button>
      <button
        onClick={() => {
          tracker.observeInterruptedRun({ runID: "run-2", taskID: "task-2" });
        }}
        type="button"
      >
        observe-untracked
      </button>
    </div>
  );
}

describe("useBoardMoveRunFeedback", () => {
  beforeEach(() => {
    push.mockClear();
  });

  it("notifies once when a tracked move run is interrupted", () => {
    render(<Harness />);
    fireEvent.click(screen.getByText("track"));
    fireEvent.click(screen.getByText("observe-tracked"));
    expect(push).toHaveBeenCalledTimes(1);
    expect(push.mock.calls[0]?.[0]).toMatchObject({
      durationMs: Infinity,
      id: "board-move-run-interrupted-run-1",
    });
  });

  it("ignores interruptions for runs that were never tracked", () => {
    render(<Harness />);
    fireEvent.click(screen.getByText("observe-untracked"));
    expect(push).not.toHaveBeenCalled();
  });

  it("does not notify twice for the same interrupted run", () => {
    render(<Harness />);
    fireEvent.click(screen.getByText("track"));
    fireEvent.click(screen.getByText("observe-tracked"));
    fireEvent.click(screen.getByText("observe-tracked"));
    expect(push).toHaveBeenCalledTimes(1);
  });
});
