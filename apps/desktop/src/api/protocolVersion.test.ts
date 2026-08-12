import protocol from "../../../../shared/protocol/version.json";
import { protocolVersion } from "./jsonRpcSocket";

it("uses the shared protocol version injected by Vite", () => {
  expect(protocolVersion).toBe(protocol.version);
});
