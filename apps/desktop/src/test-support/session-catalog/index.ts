import { z } from "zod";

import type { SessionCategory, SessionPagePosition } from "@/api";

export type SessionPageRequest = Readonly<{
  projectID: string;
  category: SessionCategory;
  position: SessionPagePosition;
}>;

const sessionPageRequestSchema = z
  .object({
    project_id: z.string(),
    category: z.enum(["main", "subagent"]),
    page_size: z.literal(100),
    position: z.discriminatedUnion("kind", [
      z.object({ kind: z.literal("newest") }).strict(),
      z.object({ kind: z.literal("older"), token: z.string() }).strict(),
      z.object({ kind: z.literal("newer"), token: z.string() }).strict(),
    ]),
  })
  .strict();

export function parseSessionPageRequest(params: unknown): SessionPageRequest {
  const value = sessionPageRequestSchema.parse(params);
  return sessionPageRequest(value.project_id, value.category, value.position);
}

export function sessionPageRequest(
  projectID: string,
  category: SessionCategory,
  position: SessionPagePosition,
): SessionPageRequest {
  return { projectID, category, position };
}

export function sessionPageFixture(
  input: Readonly<{
    projectID: string;
    category: SessionCategory;
    sessions: readonly ReturnType<typeof sessionSummaryFixture>[];
    older?: string | undefined;
    newer?: string | undefined;
  }>,
) {
  return {
    project_id: input.projectID,
    category: input.category,
    sessions: input.sessions,
    ...(input.older === undefined ? {} : { older: input.older }),
    ...(input.newer === undefined ? {} : { newer: input.newer }),
  };
}

export function sessionSummaryFixture(
  id: string,
  category: SessionCategory,
  input: Readonly<{
    name?: string | undefined;
    preview?: string | undefined;
    updatedAt?: string | undefined;
  }> = {},
) {
  return {
    session_id: id,
    category,
    ...(input.name === undefined ? {} : { name: input.name }),
    ...(input.preview === undefined ? {} : { first_prompt_preview: input.preview }),
    updated_at: input.updatedAt ?? "2026-08-11T20:00:00Z",
  };
}

export function singleSessionPageFixture(
  input: Readonly<{
    projectID: string;
    category: SessionCategory;
    sessionID: string;
    older: string | null;
    newer: string | null;
    name?: string | undefined;
    preview?: string | undefined;
    updatedAt?: string | undefined;
  }>,
) {
  return sessionPageFixture({
    projectID: input.projectID,
    category: input.category,
    sessions: [
      sessionSummaryFixture(input.sessionID, input.category, {
        ...(input.name === undefined ? {} : { name: input.name }),
        ...(input.preview === undefined ? {} : { preview: input.preview }),
        ...(input.updatedAt === undefined ? {} : { updatedAt: input.updatedAt }),
      }),
    ],
    ...(input.older === null ? {} : { older: input.older }),
    ...(input.newer === null ? {} : { newer: input.newer }),
  });
}
