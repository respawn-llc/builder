import { createJsonRpcTransport } from "./jsonRpc";
import {
  ContractError,
  ProtocolMismatchError,
  RpcError,
  ServerRootMismatchError,
  decodeWorkflowLabelError,
} from "./errors";
import { protocolVersionMismatchErrorCode, subscriptionCompleteMethod } from "./jsonRpcSocket";
import { z } from "zod";

type SentFrame = Readonly<{
  id: string;
  method: string;
}>;

const sentFrameSchema = z.object({
  id: z.string(),
  method: z.string(),
});

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
    vi.useRealTimers();
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
    errorAck(socket, 0, {
      code: protocolVersionMismatchErrorCode,
      message: "unsupported protocol version",
    });

    await expect(readiness).rejects.toBeInstanceOf(ProtocolMismatchError);
    expect(socket.sent).toHaveLength(1);
    expect(frame(socket, 0)).toMatchObject({ method: "protocol.handshake" });
  });

  it("preserves structured JSON-RPC error data on control calls", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const request = transport.call("workflow.project.label.create", {
      project_id: "project-1",
      name: "Priority",
    });
    const socket = sockets[0] ?? failTest("control socket missing");

    socket.open();
    await waitForSent(socket, 1);
    ack(socket, 0);
    await waitForSent(socket, 2);
    errorAck(socket, 1, {
      code: -32031,
      message: "label name already exists",
      data: {
        type: "workflow_label_error",
        reason: "name_conflict",
        project_id: "project-1",
      },
    });

    const error = await request.catch((cause: unknown) => cause);
    expect(error).toBeInstanceOf(RpcError);
    expect(error).toMatchObject({
      code: -32031,
      method: "workflow.project.label.create",
      data: {
        type: "workflow_label_error",
        reason: "name_conflict",
        project_id: "project-1",
      },
    });
    expect(decodeWorkflowLabelError(error)).toMatchObject({
      reason: "name_conflict",
      projectID: "project-1",
    });
  });

  it("runs dedicated calls on a one-use socket without disturbing the control socket", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const readiness = transport.call("server.readiness.get", {});
    const controlSocket = sockets[0] ?? failTest("control socket missing");
    controlSocket.open();
    await waitForSent(controlSocket, 1);
    ack(controlSocket, 0);
    await waitForSent(controlSocket, 2);
    ack(controlSocket, 1);
    await expect(readiness).resolves.toEqual({});

    const search = transport.callDedicated("workflow.task.search", { query: "needle" });
    const dedicatedSocket = sockets[1] ?? failTest("dedicated socket missing");
    dedicatedSocket.open();
    await waitForSent(dedicatedSocket, 1);
    ack(dedicatedSocket, 0);
    await waitForSent(dedicatedSocket, 2);
    expect(frame(dedicatedSocket, 1)).toMatchObject({ method: "workflow.task.search" });
    ack(dedicatedSocket, 1);

    await expect(search).resolves.toEqual({});
    expect(dedicatedSocket.readyState).toBe(MockWebSocket.CLOSED);
    expect(controlSocket.readyState).toBe(MockWebSocket.OPEN);
  });

  it("attaches a dedicated Session before a Session-scoped call", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const answer = transport.callAttachedSession("session-1", "prompt.answerBatch", {
      session_id: "session-1",
    });
    const socket = sockets[0] ?? failTest("attached Session socket missing");

    socket.open();
    await waitForSent(socket, 1);
    ack(socket, 0);
    await waitForSent(socket, 2);
    expect(frame(socket, 1)).toMatchObject({ method: "session.attach" });
    ack(socket, 1);
    await waitForSent(socket, 3);
    expect(frame(socket, 2)).toMatchObject({ method: "prompt.answerBatch" });
    ack(socket, 2);

    await expect(answer).resolves.toEqual({});
    expect(socket.readyState).toBe(MockWebSocket.CLOSED);
  });

  it("does not send a Session-scoped call when Session attachment fails", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const answer = transport.callAttachedSession("session-1", "prompt.answerBatch", {
      session_id: "session-1",
    });
    const socket = sockets[0] ?? failTest("attached Session socket missing");

    socket.open();
    await waitForSent(socket, 1);
    ack(socket, 0);
    await waitForSent(socket, 2);
    errorAck(socket, 1, { code: -32602, message: "session unavailable" });

    await expect(answer).rejects.toThrow("session unavailable");
    expect(socket.sent).toHaveLength(2);
    expect(socket.readyState).toBe(MockWebSocket.CLOSED);
  });

  it("cancels a dedicated call by closing only its socket", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const controller = new AbortController();
    const search = transport.callDedicated(
      "workflow.task.search",
      { query: "needle" },
      { signal: controller.signal },
    );
    const socket = sockets[0] ?? failTest("dedicated socket missing");
    socket.open();
    await waitForSent(socket, 1);
    ack(socket, 0);
    await waitForSent(socket, 2);

    controller.abort();

    await expect(search).rejects.toThrow("canceled");
    expect(socket.readyState).toBe(MockWebSocket.CLOSED);
  });

  it("falls back to a generic RPC error when error data is missing", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const request = transport.call("workflow.project.label.create", {
      project_id: "project-1",
      name: "Priority",
    });
    const socket = sockets[0] ?? failTest("control socket missing");

    socket.open();
    await waitForSent(socket, 1);
    ack(socket, 0);
    await waitForSent(socket, 2);
    errorAck(socket, 1, { code: -32031, message: "label request failed" });

    const error = await request.catch((cause: unknown) => cause);
    expect(error).toBeInstanceOf(RpcError);
    expect(error).toMatchObject({
      code: -32031,
      method: "workflow.project.label.create",
      data: undefined,
    });
  });

  it("falls back to a generic RPC error when error data is not valid JSON", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const request = transport.call("workflow.project.label.create", {
      project_id: "project-1",
      name: "Priority",
    });
    const socket = sockets[0] ?? failTest("control socket missing");

    socket.open();
    await waitForSent(socket, 1);
    ack(socket, 0);
    await waitForSent(socket, 2);
    const sent = frame(socket, 1);
    socket.receive(
      `{"jsonrpc":"2.0","id":${JSON.stringify(sent.id)},"error":{"code":-32031,"message":"label request failed","data":{"limit":1e400}}}`,
    );

    const error = await request.catch((cause: unknown) => cause);
    expect(error).toBeInstanceOf(RpcError);
    expect(error).toMatchObject({
      code: -32031,
      method: "workflow.project.label.create",
      data: undefined,
    });
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

  it("keeps no-timeout control calls pending past the generic request deadline", async () => {
    vi.useFakeTimers();
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const mutation = transport.call("workflow.task.start", { task_id: "task-1" }, { timeoutMs: null });
    let settled = false;
    mutation.then(
      () => {
        settled = true;
      },
      () => {
        settled = true;
      },
    );
    const socket = sockets[0] ?? failTest("control socket missing");

    socket.open();
    await waitForSent(socket, 1);
    ack(socket, 0);
    await waitForSent(socket, 2);
    expect(frame(socket, 1)).toMatchObject({ method: "workflow.task.start" });

    await vi.advanceTimersByTimeAsync(31_000);
    expect(settled).toBe(false);
    ack(socket, 1);

    await expect(mutation).resolves.toEqual({});
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
    errorAck(socket, 0, {
      code: protocolVersionMismatchErrorCode,
      message: "unsupported protocol version",
    });

    await vi.waitFor(() => {
      expect(errors[0]).toBeInstanceOf(ProtocolMismatchError);
    });
    expect(socket.sent).toHaveLength(1);
    expect(frame(socket, 0)).toMatchObject({ method: "protocol.handshake" });
    expect(socket.readyState).toBe(MockWebSocket.CLOSED);
    await new Promise((resolve) => setTimeout(resolve, 600));
    expect(sockets).toHaveLength(1);
    subscription.close();
  });

  it("does not reopen a subscription after server identity mismatch", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc", "expected-root");
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
    ackHandshakeRoot(socket, 0, "other-root");
    await vi.waitFor(() => {
      expect(errors[0]).toBeInstanceOf(ServerRootMismatchError);
    });
    await new Promise((resolve) => setTimeout(resolve, 600));

    expect(errors).toHaveLength(1);
    expect(sockets).toHaveLength(1);
    subscription.close();
  });

  it("does not reopen a subscription after a malformed frame", async () => {
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
    ack(socket, 0);
    await waitForSent(socket, 2);
    ack(socket, 1);
    await flushPromises();
    socket.receive("{");
    await vi.waitFor(() => {
      expect(errors[0]).toBeInstanceOf(ContractError);
    });
    await new Promise((resolve) => setTimeout(resolve, 600));

    expect(errors).toHaveLength(1);
    expect(sockets).toHaveLength(1);
    subscription.close();
  });

  it.each([
    ["invalid notification envelope", JSON.stringify({ jsonrpc: "1.0", method: "workflow.project" })],
    [
      "malformed completion params",
      JSON.stringify({
        jsonrpc: "2.0",
        method: "workflow.project.complete",
        params: { code: "0" },
      }),
    ],
  ])("reports %s once and does not reopen", async (_case, frameData) => {
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
    ack(socket, 0);
    await waitForSent(socket, 2);
    ack(socket, 1);
    await flushPromises();
    socket.receive(frameData);
    await vi.waitFor(() => {
      expect(errors[0]).toBeInstanceOf(ContractError);
    });
    await new Promise((resolve) => setTimeout(resolve, 600));

    expect(errors).toHaveLength(1);
    expect(sockets).toHaveLength(1);
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

  it("reopens subscription socket after an approved stream discontinuity", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const completions: number[] = [];
    const errors: Error[] = [];
    const subscription = transport.subscribe(
      "workflow.subscribeProject",
      { project_id: "project-1" },
      {
        onEvent() {
          return;
        },
        onComplete(code) {
          completions.push(code);
        },
        onError(error) {
          errors.push(error);
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
        params: { code: -32010, message: "stream gap" },
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
    expect(completions).toEqual([-32010]);
    expect(errors).toHaveLength(1);
    subscription.close();
  });

  it("does not reopen subscriptions after a nonrecoverable complete frame", async () => {
    const transport = createJsonRpcTransport("ws://127.0.0.1:53082/rpc");
    const completions: number[] = [];
    const errors: Error[] = [];
    const subscription = transport.subscribe(
      "attention.notification.subscribe",
      {},
      {
        onEvent() {
          return;
        },
        onComplete(code) {
          completions.push(code);
        },
        onError(error) {
          errors.push(error);
        },
      },
    );

    const firstSocket = sockets[0] ?? failTest("attention subscription socket missing");
    firstSocket.open();
    await waitForSent(firstSocket, 1);
    ack(firstSocket, 0);
    await waitForSent(firstSocket, 2);
    ack(firstSocket, 1);
    await flushPromises();

    firstSocket.receive(
      JSON.stringify({
        jsonrpc: "2.0",
        method: "attention.notification.complete",
        params: { code: -32012, message: "stream failed" },
      }),
    );

    await flushPromises();
    await new Promise((resolve) => setTimeout(resolve, 600));
    expect(sockets).toHaveLength(1);
    expect(completions).toEqual([-32012]);
    expect(errors).toHaveLength(1);
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

  it("maps attention notification subscriptions to their complete method", () => {
    expect(subscriptionCompleteMethod("attention.notification.subscribe")).toBe(
      "attention.notification.complete",
    );
  });
});

function ack(socket: MockWebSocket, sentIndex: number): void {
  const sent = frame(socket, sentIndex);
  socket.receive(JSON.stringify({ jsonrpc: "2.0", id: sent.id, result: {} }));
}

function errorAck(
  socket: MockWebSocket,
  sentIndex: number,
  error: Readonly<{ code: number; message: string; data?: unknown }>,
): void {
  const sent = frame(socket, sentIndex);
  socket.receive(JSON.stringify({ jsonrpc: "2.0", id: sent.id, error }));
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
  return sentFrameSchema.safeParse(value).success;
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
