import { z } from "zod";

import type { SessionCategory, SessionPagePosition } from "@/api";

export type SessionPageRequest = Readonly<{
  projectID: string;
  category: SessionCategory;
  position: SessionPagePosition;
}>;

const sessionPageRequestSchema = z.strictObject({
  project_id: z.string(),
  category: z.enum(["main", "subagent"]),
  page_size: z.literal(100),
  position: z.discriminatedUnion("kind", [
    z.strictObject({ kind: z.literal("newest") }),
    z.strictObject({ kind: z.literal("older"), token: z.string() }),
    z.strictObject({ kind: z.literal("newer"), token: z.string() }),
  ]),
});

export function parseSessionPageRequest(params: unknown): SessionPageRequest {
  const value = sessionPageRequestSchema.parse(params);
  return { projectID: value.project_id, category: value.category, position: value.position };
}

type SessionFixture = readonly [id: string, name?: string, preview?: string];

export function sessionPageFixture(
  scope: Pick<SessionPageRequest, "projectID" | "category">,
  sessions: readonly SessionFixture[],
  cursors: Readonly<{ older?: string | undefined; newer?: string | undefined }> = {},
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
    older: cursors.older,
    newer: cursors.newer,
  };
}
