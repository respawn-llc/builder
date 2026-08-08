import { z } from "zod";

import {
  registeredWorktreeTopologySchema,
  retainedPreviousWorktreeSchema,
  type RegisteredWorktreeTopology,
  type RetainedPreviousWorktree,
} from "./worktreeTopology";

export type WorktreeSetupFailureCause =
  | Readonly<{ kind: "process_exit"; exitCode: number; stdout: string | null; stderr: string | null }>
  | Readonly<{ kind: "timeout"; stdout: string | null; stderr: string | null }>
  | Readonly<{
      kind:
        | "target_preparation"
        | "interruption_persistence"
        | "canceled"
        | "controller_shutdown"
        | "operational";
    }>;

export type WorktreeSetupFailure = Readonly<{
  retryReadiness: "retry_ready" | "non_retryable";
  cause: WorktreeSetupFailureCause;
  diagnostic: string;
  retainedWorktree: RegisteredWorktreeTopology | null;
  retainedPreviousWorktree: RetainedPreviousWorktree | null;
}>;

const processExitCauseSchema = z
  .object({
    kind: z.literal("process_exit"),
    process_exit: z
      .object({
        exit_code: z
          .number()
          .int()
          .refine((value) => value !== 0),
        stdout: z.string().nullable().optional(),
        stderr: z.string().nullable().optional(),
      })
      .strict(),
  })
  .strict()
  .transform((value): WorktreeSetupFailureCause => ({
    kind: value.kind,
    exitCode: value.process_exit.exit_code,
    stdout: value.process_exit.stdout ?? null,
    stderr: value.process_exit.stderr ?? null,
  }));

const timeoutCauseSchema = z
  .object({
    kind: z.literal("timeout"),
    timeout: z
      .object({
        stdout: z.string().nullable().optional(),
        stderr: z.string().nullable().optional(),
      })
      .strict(),
  })
  .strict()
  .transform((value): WorktreeSetupFailureCause => ({
    kind: value.kind,
    stdout: value.timeout.stdout ?? null,
    stderr: value.timeout.stderr ?? null,
  }));

const markerCauseSchema = (
  kind:
    "target_preparation" | "interruption_persistence" | "canceled" | "controller_shutdown" | "operational",
) =>
  z
    .object({
      kind: z.literal(kind),
      [kind]: z.object({}).strict(),
    })
    .strict()
    .transform((): WorktreeSetupFailureCause => ({ kind }));

const failureCauseSchema = z.discriminatedUnion("kind", [
  processExitCauseSchema,
  timeoutCauseSchema,
  markerCauseSchema("target_preparation"),
  markerCauseSchema("interruption_persistence"),
  markerCauseSchema("canceled"),
  markerCauseSchema("controller_shutdown"),
  markerCauseSchema("operational"),
]);

export const worktreeSetupFailureWireSchema = z
  .object({
    retry_readiness: z.enum(["retry_ready", "non_retryable"]),
    cause: failureCauseSchema,
    diagnostic: z.string().trim().min(1),
    retained_worktree: registeredWorktreeTopologySchema.nullable().optional(),
    retained_previous_worktree: retainedPreviousWorktreeSchema.nullable().optional(),
  })
  .strict()
  .superRefine((value, context) => {
    const retryable =
      value.cause.kind === "process_exit" ||
      value.cause.kind === "timeout" ||
      value.cause.kind === "target_preparation";
    const nonRetryable =
      value.cause.kind === "interruption_persistence" ||
      value.cause.kind === "canceled" ||
      value.cause.kind === "controller_shutdown";
    if (
      (retryable && value.retry_readiness !== "retry_ready") ||
      (nonRetryable && value.retry_readiness !== "non_retryable")
    ) {
      context.addIssue({
        code: "custom",
        message: "Failure retry readiness does not match its typed cause.",
        path: ["retry_readiness"],
      });
    }
    if (
      value.retry_readiness === "retry_ready" &&
      value.cause.kind !== "target_preparation" &&
      value.retained_worktree == null
    ) {
      context.addIssue({
        code: "custom",
        message: "Retry-ready setup-script failure requires a retained Worktree.",
        path: ["retained_worktree"],
      });
    }
  })
  .transform(
    (value): WorktreeSetupFailure => ({
      retryReadiness: value.retry_readiness,
      cause: value.cause,
      diagnostic: value.diagnostic,
      retainedWorktree: value.retained_worktree ?? null,
      retainedPreviousWorktree: value.retained_previous_worktree ?? null,
    }),
  );
