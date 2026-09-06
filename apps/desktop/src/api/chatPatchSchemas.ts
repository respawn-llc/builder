import { z } from "zod";

const pathSchema = z.object({ Absolute: z.string().min(1), Relative: z.string().min(1) }).strict();

const changedLineSchema = z.object({ Kind: z.enum(["added", "removed"]), Content: z.string() }).strict();

const changeGroupSchema = z.object({ Lines: z.array(changedLineSchema).min(1) }).strict();

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

const groupsSchema = z
  .array(changeGroupSchema)
  .nullable()
  .transform((groups) => groups ?? []);

const addOperationSchema = z
  .object({
    Kind: z.literal("add"),
    Source: z.null(),
    Groups: groupsSchema.refine(
      (groups) =>
        groups.length > 0 && groups.every((group) => group.Lines.every((line) => line.Kind === "added")),
    ),
    Deletion: z.null(),
  })
  .strict();

const updateOperationSchema = z
  .object({
    Kind: z.literal("update"),
    Source: z.null(),
    Groups: groupsSchema,
    Deletion: z.null(),
  })
  .strict();

const moveOperationSchema = z
  .object({
    Kind: z.literal("move"),
    Source: pathSchema,
    Groups: groupsSchema,
    Deletion: z.null(),
  })
  .strict();

const deleteOperationSchema = z
  .object({
    Kind: z.literal("delete"),
    Source: z.null(),
    Groups: groupsSchema.refine((groups) => groups.length === 0),
    Deletion: wholeFileDeletionOperationSchema,
  })
  .strict();

const fileOperationSchema = z.discriminatedUnion("Kind", [
  addOperationSchema,
  updateOperationSchema,
  moveOperationSchema,
  deleteOperationSchema,
]);

type FileOperation = z.output<typeof fileOperationSchema>;

function changedLineCount(operations: readonly FileOperation[], kind: "added" | "removed") {
  return operations.reduce(
    (total, operation) =>
      total +
      operation.Groups.reduce(
        (operationTotal, group) => operationTotal + group.Lines.filter((line) => line.Kind === kind).length,
        0,
      ),
    0,
  );
}

function deletionLineCount(operations: readonly FileOperation[]) {
  const countedGroups = new Set<number>();
  let removed = 0;
  for (const operation of operations) {
    const disposition = operation.Deletion?.disposition;
    if (disposition === null || disposition === undefined) continue;
    const group = disposition.physical_group.first_operation.hunk_ordinal;
    if (countedGroups.has(group)) continue;
    countedGroups.add(group);
    removed += disposition.removed;
  }
  return removed;
}

function hasPendingDeletion(operations: readonly FileOperation[]) {
  return operations.some(
    (operation) => operation.Deletion !== null && operation.Deletion.disposition === null,
  );
}

const fileChangeSchema = z
  .object({
    Path: pathSchema,
    Added: z.number().int().nonnegative(),
    Removed: z.number().int().nonnegative().nullable(),
    Operations: z.array(fileOperationSchema).min(1),
  })
  .strict()
  .superRefine((file, context) => {
    const added = changedLineCount(file.Operations, "added");
    if (file.Added !== added) {
      context.addIssue({ code: "custom", path: ["Added"], message: "added count does not match lines" });
    }
    const expectedRemoved = hasPendingDeletion(file.Operations)
      ? null
      : changedLineCount(file.Operations, "removed") + deletionLineCount(file.Operations);
    if (file.Removed !== expectedRemoved) {
      context.addIssue({
        code: "custom",
        path: ["Removed"],
        message: "removed count does not match lines and deletion facts",
      });
    }
  });

const changesPresentationSchema = z
  .object({
    Variant: z.literal("changes"),
    Files: z
      .array(fileChangeSchema)
      .min(1)
      .refine(
        (files) => new Set(files.map((file) => file.Path.Absolute)).size === files.length,
        "absolute file paths must be unique",
      ),
    InvalidInput: z.null(),
  })
  .strict();

const invalidInputPresentationSchema = z
  .object({
    Variant: z.literal("invalid_input"),
    InvalidInput: z.object({ InputDetail: z.string() }).strict(),
  })
  .strict();

export const patchPresentationSchema = z.discriminatedUnion("Variant", [
  changesPresentationSchema,
  invalidInputPresentationSchema,
]);
