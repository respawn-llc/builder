import type { ApiService } from "./apiService";
import { ApiClient } from "./client";
import { FakeRpcTransport } from "@/test-support/api";

describe("ApiClient connection state", () => {
  it("exposes connection state through the feature-safe API port", () => {
    const transport = new FakeRpcTransport([]);
    const service: ApiService = new ApiClient(transport);

    expect(service.connection.snapshot()).toMatchObject({ phase: "connected" });

    transport.connection.set("disconnected", "offline");

    expect(service.connection.snapshot()).toMatchObject({
      phase: "disconnected",
      lastError: "offline",
    });
  });
});
