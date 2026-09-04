import { create, decodeJson, encodeJson, operationName } from "@app/server-api-contract";
import {
  ControlService,
  ListSuccessSchema,
  ViewService,
  type BackgroundProcess,
} from "@app/server-api-contract/gen/kent/api/process/process_pb";

import { timestampMillis } from "./clientTime";
import { ContractError } from "./errors";
import { jsonValueSchema } from "./json";
import type { DesktopProcess } from "./processes";
import type { DescriptorRpcTransport } from "./transport";

export async function listProcesses(
  transport: DescriptorRpcTransport,
  projectID: string,
): Promise<readonly DesktopProcess[]> {
  const method = ViewService.method.list;
  const request = create(method.input, { projectId: projectID.trim() });
  const raw = await transport.call(operationName(method), encodeJson(method.input, request));
  try {
    const success = decodeJson(ListSuccessSchema, jsonValueSchema.parse(raw));
    return success.processes.map(processFromGenerated);
  } catch {
    throw new ContractError(`${operationName(method)} response did not match GUI contract.`);
  }
}

export async function killProcess(transport: DescriptorRpcTransport, processID: string): Promise<void> {
  const method = ControlService.method.kill;
  const request = create(method.input, { processId: processID.trim() });
  await transport.call(operationName(method), encodeJson(method.input, request));
}

function processFromGenerated(process: BackgroundProcess): DesktopProcess {
  if (process.startedAt === undefined) {
    throw new Error("Process start time is required.");
  }
  return {
    id: process.id,
    state: process.state,
    command: process.command,
    workdir: process.workdir,
    startedAt: timestampMillis(process.startedAt),
    finishedAt: process.finishedAt === undefined ? null : timestampMillis(process.finishedAt),
    exitCode: process.exitCode ?? null,
    recentOutput: process.recentOutput,
    running: process.running,
    killRequested: process.killRequested,
  };
}
