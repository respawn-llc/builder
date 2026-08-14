import { describe, expect, it, vi } from "vitest";

import { createAppQueryClient } from "./queryClient";

describe("application query client", () => {
  it("runs failed reads once and leaves retry to an explicit request", async () => {
    const queryClient = createAppQueryClient();
    const load = vi.fn().mockRejectedValue(new Error("unavailable"));
    const request = {
      queryFn: load,
      queryKey: ["one-attempt-read"] as const,
    };

    await expect(queryClient.fetchQuery(request)).rejects.toThrow("unavailable");
    expect(load).toHaveBeenCalledOnce();

    await expect(queryClient.fetchQuery(request)).rejects.toThrow("unavailable");
    expect(load).toHaveBeenCalledTimes(2);
  });
});
