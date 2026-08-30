import { z } from "zod";

const renderedLineSchema = z
  .object({
    Kind: z.enum(["header", "file", "diff", "raw"]),
    Text: z.string(),
    FileIndex: z.number().int(),
    Path: z.string(),
  })
  .strict();

const wholeFileDeletionOperationIDSchema = z
  .object({ hunk_ordinal: z.number().int().nonnegative() })
  .strict();

const wholeFileDeletionGroupIDSchema = z
  .object({ first_operation: wholeFileDeletionOperationIDSchema })
  .strict();

const wholeFileDeletionDispositionSchema = z
  .object({
    physical_group: wholeFileDeletionGroupIDSchema,
    removed: z.number().int().nonnegative(),
  })
  .strict();

const wholeFileDeletionOperationSchema = z
  .object({
    id: wholeFileDeletionOperationIDSchema,
    disposition: wholeFileDeletionDispositionSchema.nullable(),
  })
  .strict();

const renderedFileSchema = z
  .object({
    AbsPath: z.string(),
    RelPath: z.string(),
    Added: z.number().int().nonnegative(),
    Removed: z.number().int().nonnegative(),
    Diff: z.array(z.string()).nullable(),
    WholeFileDeletions: z.array(wholeFileDeletionOperationSchema).nullable(),
  })
  .strict();

export const renderedPatchSchema = z
  .object({
    Files: z.array(renderedFileSchema).nullable(),
    SummaryLines: z.array(renderedLineSchema).nullable(),
    DetailLines: z.array(renderedLineSchema).nullable(),
  })
  .strict();
