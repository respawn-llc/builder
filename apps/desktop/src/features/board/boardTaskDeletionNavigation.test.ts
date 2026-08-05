import { finishBoardTaskDeletion } from "./boardTaskDeletionNavigation";

describe("finishBoardTaskDeletion", () => {
  it("surfaces a committed navigation failure and dismisses the confirmation", () => {
    const close = vi.fn();
    const onNavigationError = vi.fn();
    const navigationError = new Error("navigation failed");

    finishBoardTaskDeletion({
      close,
      navigationResult: { error: navigationError, status: "failed" },
      onNavigationError,
    });

    expect(onNavigationError).toHaveBeenCalledWith(navigationError);
    expect(close).toHaveBeenCalledOnce();
  });
});
