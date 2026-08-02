import { act, renderHook, waitFor } from "@testing-library/react";
import { vi } from "vitest";

import type { TaskMovePreviewResponse } from "@/api";
import { useManualMoveController } from "./useManualMoveController";

describe("useManualMoveController", () => {
  it("disables other board actions while a preview or dialog is pending", async () => {
    let resolvePreview: ((response: TaskMovePreviewResponse) => void) | undefined;
    const previewPromise = new Promise<TaskMovePreviewResponse>((resolve) => {
      resolvePreview = resolve;
    });
    const { result } = renderHook(() =>
      useManualMoveController({
        api: {
          previewMoveTask: vi.fn(async () => previewPromise),
        },
        onPreviewBlocked: vi.fn(),
        onPreviewError: vi.fn(),
        runAction: vi.fn(),
      }),
    );

    act(() => {
      result.current.preview("task-1", "node-2");
    });
    expect(result.current.actionsDisabled).toBe(true);

    act(() => {
      resolvePreview?.({
        outcome: "transition",
        transition: {
          choices: [
            {
              transitionKey: "next",
              label: "Next",
              sourceNodeDisplayName: "Plan",
              requiredValues: [],
            },
          ],
        },
      });
    });
    await waitFor(() => {
      expect(result.current.pending).not.toBeNull();
    });
    expect(result.current.actionsDisabled).toBe(true);

    act(() => {
      result.current.cancel();
    });
    expect(result.current.actionsDisabled).toBe(false);
  });
});
