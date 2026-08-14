import { CancelledError } from "@tanstack/react-query";

export function reportNonCancelledError(
  error: unknown,
  report: (error: unknown) => void,
): void {
  if (error instanceof CancelledError) {
    return;
  }
  report(error);
}
