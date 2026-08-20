import { z } from "zod";
import type { SessionCategory } from "../models";
export const canonicalProjectIDSchema = z
  .string()
  .refine((value) => value.trim().length > 0 && value.trim() === value);
export const workspaceOffsetSchema = z.number().int().nonnegative();
export const sessionCategorySchema: z.ZodType<SessionCategory> = z.enum(["main", "subagent"]);
export const sessionPageOffsetSchema = z.number().int().nonnegative();
