import { type RegisteredWorktreeTopology, type RetainedPreviousWorktree } from "./schemas/worktree";
import type { SetupOperationID } from "./setupOperationID";
import type { WorkflowExecutionTargetSelection } from "./workflowExecutionTarget";

type WorktreeSetupOutput = Readonly<{ stdout: string | null; stderr: string | null }>;
export type WorktreeSetupFailureCause =
  | (Readonly<{ kind: "process_exit"; exitCode: number }> & WorktreeSetupOutput)
  | (Readonly<{ kind: "timeout" }> & WorktreeSetupOutput)
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
  scriptPath: string | null;
  executionTarget: WorkflowExecutionTargetSelection | null;
  retainedWorktree: RegisteredWorktreeTopology | null;
  retainedPreviousWorktree: RetainedPreviousWorktree | null;
}>;

export type WorktreeSetupPhase = "started" | "completed" | "not_required" | "failed";
type SetupEvent<Phase extends WorktreeSetupPhase, Payload> = Readonly<
  { setupOperationID: SetupOperationID; phase: Phase } & Payload
>;
export type WorktreeSetupEvent =
  | SetupEvent<
      "started",
      {
        started: Readonly<{
          sourceWorkspaceRoot: string;
          worktreeRoot: string;
          scriptPath: string;
        }>;
      }
    >
  | SetupEvent<
      "completed",
      { completed: Readonly<{ retainedPreviousWorktree: RetainedPreviousWorktree | null }> }
    >
  | SetupEvent<
      "not_required",
      {
        notRequired: Readonly<{
          reason: "no_target_preparation" | "no_configured_script";
          retainedPreviousWorktree: RetainedPreviousWorktree | null;
        }>;
      }
    >
  | SetupEvent<"failed", { failed: WorktreeSetupFailure }>;

export type WorktreeSetupEventHandler = Readonly<{
  onOpen?(): void;
  onEvent(event: WorktreeSetupEvent): void;
  onComplete(): void;
  onError(error: Error): void;
}>;
