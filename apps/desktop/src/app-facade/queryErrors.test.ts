import { CancelledError } from "@tanstack/react-query";
import { vi } from "vitest";

import { reportNonCancelledError } from "./queryErrors";

describe("reportNonCancelledError", () => {
  it("reports ordinary failures", () => {
    const report = vi.fn();
    const failure = new Error("refresh failed");

    reportNonCancelledError(failure, report);

    expect(report).toHaveBeenCalledWith(failure);
  });

  it("treats query cancellation as control flow", () => {
    const report = vi.fn();

    reportNonCancelledError(new CancelledError(), report);

    expect(report).not.toHaveBeenCalled();
  });
});
