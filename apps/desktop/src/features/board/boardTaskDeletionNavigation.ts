import type { AppNavigationResult } from "@/app-facade";

export function finishBoardTaskDeletion({
  close,
  navigationResult,
  onNavigationError,
}: Readonly<{
  close: () => void;
  navigationResult: AppNavigationResult;
  onNavigationError: (error: unknown) => void;
}>): void {
  if (navigationResult.status === "failed") {
    onNavigationError(navigationResult.error);
  }
  close();
}
