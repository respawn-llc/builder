import { CatalogContractError, ContractError } from "./errors";
import { create, operationName } from "@app/server-api-contract";
import {
  SessionCatalogService,
  SessionCategory as GeneratedSessionCategory,
  type SessionSummary,
} from "@app/server-api-contract/gen/kent/api/project/session_catalog_pb";
import {
  sessionCatalogPageSize,
  type SessionCatalogPage,
  type SessionCatalogSummary,
  type SessionCategory,
} from "./models";
import { requireUnarySuccess } from "./protobufRpc";
import type { DescriptorRpcTransport } from "./transport";
import { timestampMillis } from "./clientTime";

export function requireCatalogProject(method: string, expected: string, actual: string): void {
  if (actual !== expected) throw CatalogContractError.projectMismatch(method, expected, actual);
}

export async function listSessionPage(
  transport: DescriptorRpcTransport,
  projectID: string,
  category: SessionCategory,
  offset: number,
): Promise<SessionCatalogPage> {
  const method = SessionCatalogService.method.page;
  const operation = operationName(method);
  const success = requireUnarySuccess(
    method,
    await transport.callDescriptor(
      method,
      create(method.input, {
        projectId: projectID,
        category: sessionCategoryToGenerated(category),
        offset,
        limit: sessionCatalogPageSize,
      }),
    ),
  );
  if (success.sessions.length > sessionCatalogPageSize) {
    throw CatalogContractError.malformedResponse(
      operation,
      new ContractError(`Session page exceeds the requested ${String(sessionCatalogPageSize)} rows.`),
    );
  }
  const response: SessionCatalogPage = {
    projectID: success.projectId,
    category: sessionCategoryFromGenerated(success.category),
    sessions: success.sessions.map(sessionSummary),
    nextOffset: success.nextOffset ?? null,
  };
  requireCatalogProject(operation, projectID, response.projectID);
  if (response.category !== category) {
    throw CatalogContractError.sessionCategoryMismatch(operation, category, response.category);
  }
  return response;
}

function sessionSummary(summary: SessionSummary): SessionCatalogSummary {
  if (summary.updatedAt === undefined) {
    throw new ContractError("Session catalog summary timestamp is required.");
  }
  return {
    id: summary.sessionId,
    category: sessionCategoryFromGenerated(summary.category),
    name: summary.name ?? null,
    firstPromptPreview: summary.firstPromptPreview ?? null,
    updatedAt: timestampMillis(summary.updatedAt),
  };
}

function sessionCategoryToGenerated(category: SessionCategory): GeneratedSessionCategory {
  switch (category) {
    case "main":
      return GeneratedSessionCategory.MAIN;
    case "subagent":
      return GeneratedSessionCategory.SUBAGENT;
  }
}

function sessionCategoryFromGenerated(category: GeneratedSessionCategory): SessionCategory {
  switch (category) {
    case GeneratedSessionCategory.MAIN:
      return "main";
    case GeneratedSessionCategory.SUBAGENT:
      return "subagent";
    case GeneratedSessionCategory.UNSPECIFIED:
      throw new ContractError("Session category is unspecified.");
  }
}
