import { ContractError } from "@/api";

import type { CommittedRow, CompactionStatus, Hydration, RuntimeActivity } from "./types";

type ActiveStep = NonNullable<RuntimeActivity["ActiveStep"]>;
type CompactionFacts = Pick<CompactionStatus, "StepID" | "Count" | "Mode" | "RequestID">;
type CompactionAttempt = Readonly<{
  checkpoint: number;
  step: ActiveStep;
  facts: CompactionFacts | null;
}>;
type Stage = Readonly<{
  kind: "stage";
  attempt: CompactionAttempt;
  rows: readonly CommittedRow[];
  presentation: "continuous-live" | "page-only";
}>;
export type CompactionLifecycle =
  | Stage
  | Readonly<{
      kind: "reflected";
      facts: CompactionFacts;
    }>
  | null;

export function compactionStep(activity: RuntimeActivity): ActiveStep | null {
  if (activity.State !== "running" && activity.State !== "awaiting_prompt") return null;
  const step = activity.ActiveStep;
  return step?.ActiveKind === "compaction" || step?.ActiveKind === "pre_submit_compaction" ? step : null;
}

function sameStep(left: ActiveStep, right: ActiveStep): boolean {
  return left.RunID === right.RunID && left.StepID === right.StepID && left.ActiveKind === right.ActiveKind;
}

export function activityContinues(
  lifecycle: CompactionLifecycle,
  current: RuntimeActivity | null,
  next: RuntimeActivity,
): boolean {
  if (lifecycle === null) return false;
  const step = compactionStep(next);
  if (lifecycle.kind === "stage") {
    return step !== null && sameStep(lifecycle.attempt.step, step);
  }
  if (step === null) return true;
  const previous = current === null ? null : compactionStep(current);
  return previous !== null && sameStep(previous, step);
}

export function bindStatus(attempt: CompactionAttempt, facts: CompactionStatus): CompactionAttempt {
  const previous = attempt.facts;
  const expectedCount = facts.State === "completed" ? attempt.checkpoint + 1 : 0;
  if (
    facts.StepID !== attempt.step.StepID ||
    facts.Count !== expectedCount ||
    (previous !== null && !sameAttemptIdentity(previous, facts))
  ) {
    throw new ContractError("Compaction facts do not match the active attempt.");
  }
  return { ...attempt, facts };
}

function bindActiveCompaction(attempt: CompactionAttempt, facts: CompactionStatus): CompactionAttempt {
  if (
    facts.State !== "started" ||
    facts.StepID !== attempt.step.StepID ||
    facts.Count !== attempt.checkpoint + 1
  ) {
    throw new ContractError("Active compaction facts do not match the active attempt.");
  }
  return { ...attempt, facts };
}

function sameAttemptIdentity(left: CompactionFacts, right: CompactionFacts): boolean {
  return (
    left.StepID === right.StepID &&
    left.Mode === right.Mode &&
    (left.RequestID ?? null) === (right.RequestID ?? null)
  );
}

export function sameCompaction(left: CompactionFacts, right: CompactionFacts): boolean {
  return left.Count === right.Count && sameAttemptIdentity(left, right);
}

export function beginStage(checkpoint: number | null, step: ActiveStep): Stage {
  if (checkpoint === null) throw new ContractError("Compaction requires an admitted hydration baseline.");
  return {
    kind: "stage",
    attempt: { checkpoint, step, facts: null },
    rows: [],
    presentation: "continuous-live",
  };
}

/** Reattachment never carries an old feed's attempt identity into the new hydration facts. */
export function classifyHydration(hydration: Hydration): CompactionLifecycle {
  const checkpoint = hydration.SessionStatus.CompactionCount;
  const step = compactionStep(hydration.RuntimeReadModelUpdate.Activity);
  if (step === null) return null;
  const facts = hydration.ActiveCompaction;
  if (facts !== null && facts.StepID === step.StepID && facts.Count === checkpoint) {
    return { kind: "reflected", facts };
  }
  const stage = beginStage(checkpoint, step);
  return facts === null ? stage : { ...stage, attempt: bindActiveCompaction(stage.attempt, facts) };
}
