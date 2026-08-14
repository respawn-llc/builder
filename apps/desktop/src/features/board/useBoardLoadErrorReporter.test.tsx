import { CancelledError } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { beforeEach, vi } from "vitest";

const push = vi.fn();

vi.mock("@/app-facade", async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();
  return {
    ...actual,
    useStatusController: () => ({ push }),
  };
});

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

import { useBoardLoadErrorReporter } from "./useBoardLoadErrorReporter";

describe("useBoardLoadErrorReporter", () => {
  beforeEach(() => {
    push.mockReset();
  });

  it("reports refresh failures", () => {
    const { result } = renderHook(useBoardLoadErrorReporter);
    const failure = new Error("refresh failed");

    act(() => {
      result.current(failure);
    });

    expect(push).toHaveBeenCalledWith(
      expect.objectContaining({
        body: "refresh failed",
        tone: "danger",
      }),
    );
  });

  it("does not surface query cancellation", () => {
    const { result } = renderHook(useBoardLoadErrorReporter);

    act(() => {
      result.current(new CancelledError());
    });

    expect(push).not.toHaveBeenCalled();
  });
});
