import { z } from "zod";
import type { SessionCatalogPage, SessionCatalogSummary, SessionCategory } from "../models";
import { sessionCatalogPageSize } from "../models";
export const canonicalProjectIDSchema = z
  .string()
  .refine((value) => value.trim().length > 0 && value.trim() === value);
const canonicalNonBlankString = canonicalProjectIDSchema;
export const workspacePageTokenSchema = canonicalNonBlankString;
export const workspaceContinuationWireSchema = z.union([z.literal(""), canonicalNonBlankString]);
export const sessionCategorySchema: z.ZodType<SessionCategory> = z.enum(["main", "subagent"]);
export const sessionPageOffsetSchema = z.number().int().nonnegative();
const sessionNameSchema = z
  .string()
  .refine((value) => value.trim().length > 0)
  .nullable()
  .optional()
  .transform((value) => value ?? null);
const promptPreviewSchema = z
  .string()
  .optional()
  .transform((value) => value ?? null);
const sessionRecencySchema = z.iso
  .datetime({ offset: true })
  .transform((value) => Date.parse(value))
  .pipe(z.number().positive());
const sessionCatalogSummarySchema: z.ZodType<SessionCatalogSummary> = z
  .object({
    session_id: canonicalNonBlankString,
    category: sessionCategorySchema,
    name: sessionNameSchema,
    first_prompt_preview: promptPreviewSchema,
    updated_at: sessionRecencySchema,
  })
  .strict()
  .transform((value) => ({
    id: value.session_id,
    category: value.category,
    name: value.name,
    firstPromptPreview: value.first_prompt_preview,
    updatedAt: value.updated_at,
  }));
const sessionNextOffsetSchema = z
  .number()
  .int()
  .positive()
  .nullish()
  .transform((value) => value ?? null);
export const sessionPageResponseSchema: z.ZodType<SessionCatalogPage> = z
  .object({
    project_id: canonicalNonBlankString,
    category: sessionCategorySchema,
    sessions: z.array(sessionCatalogSummarySchema).max(sessionCatalogPageSize),
    next_offset: sessionNextOffsetSchema,
  })
  .strict()
  .superRefine((value, context) => {
    value.sessions.forEach((session, index) => {
      if (session.category !== value.category) {
        context.addIssue({
          code: "custom",
          path: ["sessions", index, "category"],
          message: "Session category must match page category.",
        });
      }
    });
  })
  .transform((value) => ({
    projectID: value.project_id,
    category: value.category,
    sessions: value.sessions,
    nextOffset: value.next_offset,
  }));
