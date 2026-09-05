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

export function bindAttempt(attempt: CompactionAttempt, facts: CompactionStatus): CompactionAttempt {
  const previous = attempt.facts;
  if (
    facts.StepID !== attempt.step.StepID ||
    facts.Count !== attempt.checkpoint + 1 ||
    (previous !== null && !sameCompaction(previous, facts))
  ) {
    throw new ContractError("Compaction facts do not match the active attempt.");
  }
  return { ...attempt, facts };
}

export function sameCompaction(left: CompactionFacts, right: CompactionFacts): boolean {
  return (
    left.StepID === right.StepID &&
    left.Count === right.Count &&
    left.Mode === right.Mode &&
    (left.RequestID ?? null) === (right.RequestID ?? null)
  );
}

export function beginStage(checkpoint: number | null, step: ActiveStep): Stage {
  if (checkpoint === null) throw new ContractError("Compaction requires an admitted hydration baseline.");
  return { kind: "stage", attempt: { checkpoint, step, facts: null }, rows: [] };
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
  return facts === null ? stage : { ...stage, attempt: bindAttempt(stage.attempt, facts) };
}
