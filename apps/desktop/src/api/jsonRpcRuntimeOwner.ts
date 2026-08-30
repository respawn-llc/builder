import { TransportError } from "./errors";
import type { JsonValue } from "./json";
import { openSocket, sendSocketDescriptorRequest, sendSocketRequest, setupSocket } from "./jsonRpcSocket";
import type { RuntimeOwnerContext, RuntimeOwnerOptions, SessionAttachment } from "./transport";
import type { DescMethod, MessageShape } from "@app/server-api-contract";

const socketOpenTimeoutMs = 10_000;
const rpcRequestTimeoutMs = 30_000;

export class JsonRpcRuntimeOwner {
  #endpoint: string;
  #expectedRootId: string;
  #owner: Readonly<{ socket: WebSocket; attachment: SessionAttachment }> | null = null;
  #tail: Promise<void> = Promise.resolve();

  constructor(endpoint: string, expectedRootId: string) {
    this.#endpoint = endpoint;
    this.#expectedRootId = expectedRootId;
  }

  async run<Result>(
    sessionID: string,
    options: RuntimeOwnerOptions,
    run: (context: RuntimeOwnerContext) => Promise<Result>,
  ): Promise<Result> {
    const operation = this.#tail.then(async () => this.#run(sessionID, options, run));
    this.#tail = operation.then(
      () => undefined,
      () => undefined,
    );
    return operation;
  }

  async #run<Result>(
    sessionID: string,
    options: RuntimeOwnerOptions,
    run: (context: RuntimeOwnerContext) => Promise<Result>,
  ): Promise<Result> {
    const requestedSessionID = sessionID.trim();
    if (requestedSessionID.length === 0) {
      throw new TransportError("Session attachment requires a Session ID.");
    }
    let owner = this.#owner;
    if (owner !== null && owner.attachment.sessionID !== requestedSessionID)
      throw new TransportError("Runtime owner connection is bound to another Session.");
    if (owner === null) {
      if (!options.createIfMissing) {
        throw new TransportError("Runtime owner connection is unavailable.");
      }
      const socket = await openSocket(this.#endpoint, socketOpenTimeoutMs);
      try {
        const attachment = await setupSocket(socket, {
          timeoutMilliseconds: rpcRequestTimeoutMs,
          expectedRootId: this.#expectedRootId,
          sessionID: requestedSessionID,
        });
        if (attachment === null || !("sessionID" in attachment)) {
          throw new TransportError("Session attachment was not established.");
        }
        owner = { socket, attachment };
        this.#owner = owner;
      } catch (error) {
        socket.close();
        throw error;
      }
    }

    const context: RuntimeOwnerContext = {
      attachment: owner.attachment,
      callDescriptor: async <Method extends DescMethod>(
        method: Method,
        request: MessageShape<Method["input"]>,
      ) =>
        sendSocketDescriptorRequest(owner.socket, method, request, {
          timeoutMilliseconds: rpcRequestTimeoutMs,
        }),
      call: async (method: string, params: JsonValue) => {
        try {
          return await sendSocketRequest(owner.socket, method, params, {
            timeoutMilliseconds: rpcRequestTimeoutMs,
          });
        } catch (error) {
          this.#discard(owner);
          throw error;
        }
      },
      poison: () => {
        this.#discard(owner);
      },
    };
    try {
      const result = await run(context);
      if (options.closeAfter) {
        this.#discard(owner);
      }
      return result;
    } catch (error) {
      this.#discard(owner);
      throw error;
    }
  }

  #discard(owner: Readonly<{ socket: WebSocket; attachment: SessionAttachment }>): void {
    if (this.#owner === owner) {
      this.#owner = null;
    }
    owner.socket.close();
  }
}
