import {
  decode,
  decodeEnvelope,
  decodeEnvelopeCorrelation,
  encode,
  encodeEnvelope,
  operationName,
  type DescMethod,
  type MessageShape,
} from "@app/server-api-contract";
import type { Result, TransportFailure } from "@app/server-api-contract/gen/kent/api/shared/foundation_pb";

import { TransportError } from "./errors";

export type DescriptorResponse = Readonly<
  | { kind: "result"; correlation: string; result: Result }
  | { kind: "transport_failure"; correlation: string; failure: TransportFailure }
>;

export function encodeDescriptorCall<Method extends DescMethod>(
  method: Method,
  request: MessageShape<Method["input"]>,
  correlation: string,
): Readonly<{ operation: string; bytes: Uint8Array }> {
  const operation = operationName(method);
  if (method.methodKind !== "unary") {
    throw new TransportError(`${operation} is not a unary operation.`);
  }
  const payload = encode(method.input, request);
  return {
    operation,
    bytes: encodeEnvelope({
      frame: {
        case: "call",
        value: {
          operation,
          correlation,
          payload,
        },
      },
    }),
  };
}

export function encodeDescriptorSubscriptionCall<Method extends DescMethod>(
  method: Method,
  request: MessageShape<Method["input"]>,
  correlation: string,
): Readonly<{ operation: string; bytes: Uint8Array }> {
  const operation = operationName(method);
  const payload = encode(method.input, request);
  return {
    operation,
    bytes: encodeEnvelope({
      frame: {
        case: "call",
        value: { operation, correlation, payload },
      },
    }),
  };
}

export function decodeDescriptorResponse(bytes: Uint8Array): DescriptorResponse {
  const envelope = decodeEnvelope(bytes);
  switch (envelope.frame.case) {
    case "result": {
      const correlation = envelope.frame.value.correlation;
      if (correlation === undefined) {
        throw new TransportError("Binary result correlation is required.");
      }
      return { kind: "result", correlation, result: envelope.frame.value };
    }
    case "transportFailure": {
      const correlation = envelope.frame.value.correlation;
      if (correlation === undefined) {
        throw new TransportError("Binary transport failure correlation is required.");
      }
      return { kind: "transport_failure", correlation, failure: envelope.frame.value };
    }
    case "call":
    case "notificationEvent":
    case undefined:
      throw new TransportError("Binary response envelope has an unexpected frame type.");
  }
}

export function descriptorResponseCorrelation(bytes: Uint8Array): string | undefined {
  return decodeEnvelopeCorrelation(bytes);
}

export function completeDescriptorResponse<Method extends DescMethod>(
  method: Method,
  expectedCorrelation: string,
  response: DescriptorResponse,
): MessageShape<Method["output"]> {
  const operation = operationName(method);
  if (response.correlation !== expectedCorrelation) {
    throw new TransportError(`${operation} response correlation does not match its request.`);
  }
  if (response.kind === "transport_failure") {
    throw new TransportError(
      `${operation} failed at the binary transport boundary with code ${response.failure.code.toString()}.`,
    );
  }
  if (response.result.operation !== operation) {
    throw new TransportError(`${operation} received a result for ${response.result.operation}.`);
  }
  if (response.result.payload === undefined) {
    throw new TransportError(`${operation} result payload is required.`);
  }
  return decode<Method["output"]>(method.output, response.result.payload);
}

export function binaryFrameBytes(data: unknown): Uint8Array | undefined {
  if (data instanceof ArrayBuffer) {
    return new Uint8Array(data);
  }
  if (ArrayBuffer.isView(data)) {
    return new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
  }
  return undefined;
}

export function binaryFramePayload(bytes: Uint8Array): ArrayBuffer {
  const payload = new ArrayBuffer(bytes.byteLength);
  new Uint8Array(payload).set(bytes);
  return payload;
}
