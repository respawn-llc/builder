import { z } from "zod";

import { sessionCatalogPageSize, type SessionCategory } from "@/api";

export type SessionPageRequest = Readonly<{
  projectID: string;
  category: SessionCategory;
  offset: number;
}>;

const sessionPageRequestSchema = z.strictObject({
  project_id: z.string(),
  category: z.enum(["main", "subagent"]),
  offset: z.number().int().nonnegative(),
  limit: z.literal(sessionCatalogPageSize),
});

export function parseSessionPageRequest(params: unknown): SessionPageRequest {
  const value = sessionPageRequestSchema.parse(params);
  return { projectID: value.project_id, category: value.category, offset: value.offset };
}

type SessionFixture = readonly [id: string, name?: string, preview?: string];

export function sessionPageFixture(
  scope: Pick<SessionPageRequest, "projectID" | "category">,
  sessions: readonly SessionFixture[],
  nextOffset: number | null = null,
) {
  return {
    project_id: scope.projectID,
    category: scope.category,
    sessions: sessions.map((session) => {
      const [id, name, preview] = session;
      return {
        session_id: id,
        category: scope.category,
        name,
        first_prompt_preview: preview,
        updated_at: "2026-08-11T20:00:00Z",
      };
    }),
    next_offset: nextOffset,
  };
}
