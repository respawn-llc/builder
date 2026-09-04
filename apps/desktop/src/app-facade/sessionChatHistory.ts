import { z } from "zod";

const sessionChatCatalogOriginSchema = z.object({
  category: z.enum(["main", "subagent"]),
});

export const sessionChatHistoryStateSchema = z.looseObject({
  sessionChat: z
    .object({
      catalogOrigin: sessionChatCatalogOriginSchema.nullable(),
      projectID: z.string().min(1),
    })
    .nullable()
    .optional(),
});

export type SessionChatCatalogOrigin = z.output<typeof sessionChatCatalogOriginSchema>;
export type SessionChatHistoryState = z.output<typeof sessionChatHistoryStateSchema>;
