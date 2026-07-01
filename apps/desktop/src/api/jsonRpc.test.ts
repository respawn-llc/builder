import { createJsonRpcTransport } from "./jsonRpc";
import { ProtocolMismatchError, ServerRootMismatchError } from "./errors";
import { protocolVersionMismatchErrorCode } from "./jsonRpcSocket";

type SentFrame = Readonly<{
  id: string;
  method: string;
}>;

class MockWebSocket extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;

  readonly sent: string[] = [];
  readyState = MockWebSocket.CONNECTING;

  constructor(readonly url: string) {
    super();
    sockets.push(this);
  }

  send(data: string): void {
    this.sent.push(data);
  }

  close(): void {
    this.readyState = MockWebSocket.CLOSED;
    this.dispatchEvent(new Event("close"));
  }

  open(): void {
    this.readyState = MockWebSocket.OPEN;
    this.dispatchEvent(new Event("open"));
  }

  receive(data: string): void {
    this.dispatchEvent(new MessageEvent("message", { data }));
  }
}

const sockets: MockWebSocket[] = [];

describe("JsonRpcWebSocketTransport", () => {
  beforeEach(() => {
    sockets.length = 0;
    vi.stubGlobal("WebSocket", MockWebSocket);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("rejects pending mutations on disconnect and does not replay them on reconnect", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const mutation = transport.call("workflow.task.start", { task_id: "task-1" });
    const firstSocket = sockets[0] ?? failTest("first socket missing");

    firstSocket.open();
    await waitForSent(firstSocket, 1);
    ack(firstSocket, 0);
    await waitForSent(firstSocket, 2);
    expect(frame(firstSocket, 1)).toMatchObject({ method: "workflow.task.start" });

    firstSocket.close();
    await expect(mutation).rejects.toThrow("closed");
    expect(firstSocket.sent).toHaveLength(2);

    const retry = transport.call("workflow.task.start", { task_id: "task-1" });
    const secondSocket = sockets[1] ?? failTest("second socket missing");
    secondSocket.open();
    await waitForSent(secondSocket, 1);
    ack(secondSocket, 0);
    await waitForSent(secondSocket, 2);
    expect(secondSocket.sent).toHaveLength(2);
    ack(secondSocket, 1);

    await expect(retry).resolves.toEqual({});
    expect(firstSocket.sent).toHaveLength(2);
  });

  it("rejects control calls on handshake protocol mismatch before sending the requested method", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const readiness = transport.call("server.readiness.get", {});
    const socket = sockets[0] ?? failTest("control socket missing");

    socket.open();
    await waitForSent(socket, 1);
    errorAck(socket, 0, protocolVersionMismatchErrorCode, "unsupported protocol version");

    await expect(readiness).rejects.toBeInstanceOf(ProtocolMismatchError);
    expect(socket.sent).toHaveLength(1);
    expect(frame(socket, 0)).toMatchObject({ method: "protocol.handshake" });
  });

  it("rejects control calls when the server serves a different persistence root", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc", "expected-root");
    const readiness = transport.call("server.readiness.get", {});
    const socket = sockets[0] ?? failTest("control socket missing");

    socket.open();
    await waitForSent(socket, 1);
    ackHandshakeRoot(socket, 0, "other-root");

    await expect(readiness).rejects.toBeInstanceOf(ServerRootMismatchError);
    expect(socket.sent).toHaveLength(1);
    expect(frame(socket, 0)).toMatchObject({ method: "protocol.handshake" });
  });

  it("rejects control calls when the server reports no persistence root id", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc", "expected-root");
    const readiness = transport.call("server.readiness.get", {});
    const socket = sockets[0] ?? failTest("control socket missing");

    socket.open();
    await waitForSent(socket, 1);
    ack(socket, 0);

    await expect(readiness).rejects.toBeInstanceOf(ServerRootMismatchError);
    expect(socket.sent).toHaveLength(1);
  });

  it("accepts control calls when the server serves the expected persistence root", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc", "expected-root");
    const readiness = transport.call("server.readiness.get", {});
    const socket = sockets[0] ?? failTest("control socket missing");

    socket.open();
    await waitForSent(socket, 1);
    ackHandshakeRoot(socket, 0, "expected-root");
    await waitForSent(socket, 2);
    expect(frame(socket, 1)).toMatchObject({ method: "server.readiness.get" });
    ack(socket, 1);

    await expect(readiness).resolves.toEqual({});
  });

  it("installs subscription event listener before subscribe ack can race with first event", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const events: string[] = [];
    const opens: string[] = [];

    transport.subscribe(
      "workflow.subscribeProject",
      { project_id: "project-1" },
      {
        onOpen() {
          opens.push("open");
        },
        onEvent(method) {
          events.push(method);
        },
        onComplete() {
          return;
        },
        onError(error) {
          throw error;
        },
      },
    );

    const socket = sockets[0] ?? failTest("subscription socket missing");
    socket.open();
    await waitForSent(socket, 1);
    ack(socket, 0);
    await waitForSent(socket, 2);
    expect(frame(socket, 1)).toMatchObject({ method: "workflow.subscribeProject" });

    socket.receive(
      JSON.stringify({
        jsonrpc: "2.0",
        method: "workflow.project",
        params: { event: { project_id: "project-1" } },
      }),
    );
    ack(socket, 1);
    await flushPromises();

    expect(opens).toEqual(["open"]);
    expect(events).toEqual(["workflow.project"]);
  });

  it("rejects subscriptions on handshake protocol mismatch before sending the subscribe method", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const errors: Error[] = [];
    const subscription = transport.subscribe(
      "workflow.subscribeProject",
      { project_id: "project-1" },
      {
        onEvent() {
          return;
        },
        onComplete() {
          return;
        },
        onError(error) {
          errors.push(error);
        },
      },
    );
    const socket = sockets[0] ?? failTest("subscription socket missing");

    socket.open();
    await waitForSent(socket, 1);
    errorAck(socket, 0, protocolVersionMismatchErrorCode, "unsupported protocol version");

    await vi.waitFor(() => {
      expect(errors[0]).toBeInstanceOf(ProtocolMismatchError);
    });
    expect(socket.sent).toHaveLength(1);
    expect(frame(socket, 0)).toMatchObject({ method: "protocol.handshake" });
    // A rejected handshake must close the socket; otherwise the reconnect loop
    // leaks a socket connected to the wrong server on every backoff.
    expect(socket.readyState).toBe(MockWebSocket.CLOSED);
    subscription.close();
  });

  it("reopens subscription socket after unexpected close", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const errors: string[] = [];
    const subscription = transport.subscribe(
      "workflow.subscribeProject",
      { project_id: "project-1" },
      {
        onEvent() {
          return;
        },
        onComplete() {
          return;
        },
        onError(error) {
          errors.push(error.message);
        },
      },
    );

    const firstSocket = sockets[0] ?? failTest("subscription socket missing");
    firstSocket.open();
    await waitForSent(firstSocket, 1);
    ack(firstSocket, 0);
    await waitForSent(firstSocket, 2);
    ack(firstSocket, 1);
    await flushPromises();

    firstSocket.close();
    await vi.waitFor(() => {
      expect(sockets.length).toBeGreaterThanOrEqual(2);
    });
    const secondSocket = sockets[1] ?? failTest("resubscription socket missing");
    secondSocket.open();
    await waitForSent(secondSocket, 1);
    ack(secondSocket, 0);
    await waitForSent(secondSocket, 2);

    expect(frame(secondSocket, 1)).toMatchObject({ method: "workflow.subscribeProject" });
    expect(errors).toEqual(["Subscription socket closed."]);
    subscription.close();
  });

  it("reopens subscription socket after server complete notification", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const completions: string[] = [];
    const errors: string[] = [];
    const subscription = transport.subscribe(
      "workflow.subscribeProject",
      { project_id: "project-1" },
      {
        onEvent() {
          return;
        },
        onComplete(code, message) {
          completions.push(`${code.toString()}:${message}`);
        },
        onError(error) {
          errors.push(error.message);
        },
      },
    );

    const firstSocket = sockets[0] ?? failTest("subscription socket missing");
    firstSocket.open();
    await waitForSent(firstSocket, 1);
    ack(firstSocket, 0);
    await waitForSent(firstSocket, 2);
    ack(firstSocket, 1);
    await flushPromises();

    firstSocket.receive(
      JSON.stringify({
        jsonrpc: "2.0",
        method: "workflow.project.complete",
        params: { code: 409, message: "stream gap" },
      }),
    );

    await vi.waitFor(() => {
      expect(sockets.length).toBeGreaterThanOrEqual(2);
    });
    const secondSocket = sockets[1] ?? failTest("resubscription socket missing");
    secondSocket.open();
    await waitForSent(secondSocket, 1);
    ack(secondSocket, 0);
    await waitForSent(secondSocket, 2);

    expect(frame(secondSocket, 1)).toMatchObject({ method: "workflow.subscribeProject" });
    expect(completions).toEqual(["409:stream gap"]);
    expect(errors).toEqual(["Subscription socket closed."]);
    subscription.close();
  });

  it("does not reconnect after normal server complete notification", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const completions: string[] = [];
    const errors: string[] = [];
    const subscription = transport.subscribe(
      "workflow.subscribeProject",
      { project_id: "project-1" },
      {
        onEvent() {
          return;
        },
        onComplete(code, message) {
          completions.push(`${code.toString()}:${message}`);
        },
        onError(error) {
          errors.push(error.message);
        },
      },
    );

    const socket = sockets[0] ?? failTest("subscription socket missing");
    socket.open();
    await waitForSent(socket, 1);
    ack(socket, 0);
    await waitForSent(socket, 2);
    ack(socket, 1);
    await flushPromises();

    socket.receive(
      JSON.stringify({
        jsonrpc: "2.0",
        method: "workflow.project.complete",
        params: { code: 0, message: "" },
      }),
    );
    await flushPromises();

    expect(completions).toEqual(["0:"]);
    expect(errors).toEqual([]);
    expect(sockets).toHaveLength(1);
    subscription.close();
  });

  it("keeps subscriptions active for non-terminal events ending with complete", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const events: string[] = [];
    const completions: string[] = [];
    const subscription = transport.subscribe(
      "workflow.subscribeProject",
      { project_id: "project-1" },
      {
        onEvent(method) {
          events.push(method);
        },
        onComplete(code, message) {
          completions.push(`${code.toString()}:${message}`);
        },
        onError(error) {
          throw error;
        },
      },
    );

    const socket = sockets[0] ?? failTest("subscription socket missing");
    socket.open();
    await waitForSent(socket, 1);
    ack(socket, 0);
    await waitForSent(socket, 2);
    ack(socket, 1);
    await flushPromises();

    socket.receive(
      JSON.stringify({
        jsonrpc: "2.0",
        method: "workflow.project.task.complete",
        params: { event: { project_id: "project-1" } },
      }),
    );
    await flushPromises();

    expect(events).toEqual(["workflow.project.task.complete"]);
    expect(completions).toEqual([]);
    expect(sockets).toHaveLength(1);
    subscription.close();
  });
});

function ack(socket: MockWebSocket, sentIndex: number): void {
  const sent = frame(socket, sentIndex);
  socket.receive(JSON.stringify({ jsonrpc: "2.0", id: sent.id, result: {} }));
}

function errorAck(socket: MockWebSocket, sentIndex: number, code: number, message: string): void {
  const sent = frame(socket, sentIndex);
  socket.receive(JSON.stringify({ jsonrpc: "2.0", id: sent.id, error: { code, message } }));
}

function ackHandshakeRoot(socket: MockWebSocket, sentIndex: number, rootId: string): void {
  const sent = frame(socket, sentIndex);
  socket.receive(
    JSON.stringify({
      jsonrpc: "2.0",
      id: sent.id,
      result: { identity: { persistence_root_id: rootId } },
    }),
  );
}

function frame(socket: MockWebSocket, sentIndex: number): SentFrame {
  const raw = socket.sent[sentIndex] ?? failTest(`sent frame ${sentIndex.toString()} missing`);
  const parsed: unknown = JSON.parse(raw);
  if (!isSentFrame(parsed)) {
    throw new Error("Mock WebSocket frame missing id or method.");
  }
  return { id: parsed.id, method: parsed.method };
}

function isSentFrame(value: unknown): value is SentFrame {
  return (
    typeof value === "object" &&
    value !== null &&
    "id" in value &&
    "method" in value &&
    typeof value.id === "string" &&
    typeof value.method === "string"
  );
}

async function flushPromises(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

async function waitForSent(socket: MockWebSocket, count: number): Promise<void> {
  await vi.waitFor(() => {
    expect(socket.sent.length).toBeGreaterThanOrEqual(count);
  });
}

function failTest(message: string): never {
  throw new Error(message);
}
