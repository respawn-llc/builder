import { FakeRpcTransport } from "@/test-support/api";
import { ApiClient } from "./client";

it("attaches the Session for Session-scoped pending prompt reads", async () => {
  const transport = new FakeRpcTransport([
    {
      method: "ask.listPendingBySession",
      result: { Asks: [] },
    },
  ]);
  const client = new ApiClient(transport);

  await expect(client.listPendingAsks("session-1")).resolves.toEqual([]);

  expect(transport.calls).toEqual([]);
  expect(transport.attachedSessionCalls).toEqual([
    {
      sessionID: "session-1",
      method: "ask.listPendingBySession",
      params: { SessionID: "session-1" },
    },
  ]);
});
