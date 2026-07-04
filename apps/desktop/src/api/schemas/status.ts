import { z } from "zod";

import type { ServerReadiness } from "../models";

export const readinessSchema: z.ZodType<ServerReadiness> = z
  .object({
    ready: z.boolean(),
    server_id: z.string(),
    server_version: z.string(),
    server_build: z.string().optional().default(""),
    protocol_version: z.string(),
    auth_ready: z.boolean(),
    auth_required: z.boolean(),
    endpoint: z.string(),
    subagent_roles: z
      .array(
        z.object({
          name: z.string(),
        }),
      )
      .nullish()
      .transform((value) => value ?? []),
    causes: z
      .array(
        z.object({
          code: z.string(),
          severity: z.string(),
          summary: z.string().optional(),
          next_action: z.string().optional(),
          diagnostic_id: z.string().optional().default(""),
        }),
      )
      .nullish()
      .transform((value) => value ?? []),
  })
  .transform((value) => ({
    ready: value.ready,
    serverID: value.server_id,
    serverVersion: value.server_version,
    serverBuild: value.server_build,
    protocolVersion: value.protocol_version,
    authReady: value.auth_ready,
    authRequired: value.auth_required,
    endpoint: value.endpoint,
    subagentRoles: value.subagent_roles.map((role) => ({
      name: role.name,
    })),
    causes: value.causes.map((cause) => ({
      code: cause.code,
      severity: cause.severity,
      ...(cause.summary === undefined ? {} : { summary: cause.summary }),
      ...(cause.next_action === undefined ? {} : { nextAction: cause.next_action }),
      diagnosticID: cause.diagnostic_id,
    })),
  }));
