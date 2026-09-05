import { ContractError } from "@/api";

import { mergeRows, rowBatch, shareSegment, validateAdjacent, validateTail, withLivePool } from "./segments";
import type { TranscriptProvisionalItem } from "./renderSlots";
import { committedCorrelation, hydratedLive, present, reduceLive } from "./live";
import {
  beginStage,
  bindAttempt,
  classifyHydration,
  compactionStep,
  sameCompaction,
  activityContinues,
  type CompactionLifecycle,
} from "./compaction";
import type {
  Segment,
  CommittedRow,
  Hydration,
  RuntimeActivity,
  CompactionStatus,
  ResidentSegments,
  TranscriptBoundary,
  TranscriptDirection,
  TranscriptPageRequest,
  TranscriptWindowInput,
  TranscriptWindowResult,
  TranscriptWindowSnapshot,
} from "./types";

type State = Readonly<{
  segments: ResidentSegments;
  pool: readonly CommittedRow[];
  checkpoint: number | null;
  activity: RuntimeActivity | null;
  lifecycle: CompactionLifecycle;
  provisional: readonly TranscriptProvisionalItem[];
  pending: Readonly<{ request: TranscriptPageRequest; previous: TranscriptBoundary }> | null;
  snapshot: TranscriptWindowSnapshot;
}>;

function project(state: State, admitted: readonly CommittedRow[] = []): State {
  const presentation = present(state, state.snapshot.items, admitted);
  return {
    ...state,
    provisional: presentation.provisional,
    snapshot: { ...state.snapshot, items: presentation.items },
  };
}

function admitRows(state: State, rows: readonly CommittedRow[]): State {
  const batch = rowBatch(rows);
  validateTail(batch);
  rows.forEach(committedCorrelation);
  const lifecycle = state.lifecycle;
  if (lifecycle?.kind === "stage") {
    const shared = shareSegment(batch, [], lifecycle.rows);
    return { ...state, lifecycle: { ...lifecycle, rows: mergeRows(lifecycle.rows, shared.entries) } };
  }
  const shared = shareSegment(batch, state.segments, state.pool);
  const pool = mergeRows(state.pool, shared.entries);
  const segments = withLivePool(state.segments, pool);
  return project({ ...state, segments, pool }, rows);
}

function install(
  state: State,
  segments: ResidentSegments,
  operation: "replace" | "insert",
  admitted: readonly CommittedRow[],
): State {
  if (state.lifecycle?.kind !== "stage") segments = withLivePool(segments, state.pool);
  function boundary(direction: TranscriptDirection, cursor: number | null): TranscriptBoundary {
    const previous = state.snapshot[direction];
    return operation === "insert" && previous.kind === "error" && previous.cursor === cursor
      ? previous
      : { kind: "idle", cursor };
  }
  return project(
    {
      ...state,
      segments,
      pending: null,
      snapshot: {
        items: state.snapshot.items,
        older: boundary("older", segments[0]?.olderCursor ?? null),
        newer: boundary("newer", segments.at(-1)?.newerCursor ?? null),
        opening: { kind: "ready" },
      },
    },
    admitted,
  );
}

function emptyState(opening: "loading" | "disposed"): State {
  return {
    segments: [],
    pool: [],
    checkpoint: null,
    activity: null,
    lifecycle: null,
    provisional: [],
    pending: null,
    snapshot: {
      items: [],
      older: { kind: "idle", cursor: null },
      newer: { kind: "idle", cursor: null },
      opening: { kind: opening },
    },
  };
}

/** One mounted host owns this controller and executes its effects; no transport work happens here. */
export class TranscriptWindow {
  readonly openingPermit = Symbol("transcript opening");
  private state = emptyState("loading");

  get snapshot(): TranscriptWindowSnapshot {
    return this.state.snapshot;
  }

  dispatch(input: TranscriptWindowInput): TranscriptWindowResult {
    if (this.snapshot.opening.kind === "disposed") return { kind: "disposed", effects: [] };
    try {
      if (input.kind === "dispose") {
        this.state = emptyState("disposed");
        return { kind: "disposed", effects: [] };
      }
      if ("hydration" in input) {
        return this.hydrate(input.hydration, input.kind === "reattachment-hydration");
      }
      return this.reduce(input);
    } catch (error) {
      if (!(error instanceof ContractError)) throw error;
      return { kind: "contract-failure", error, effects: [] };
    }
  }

  private reduce(
    input: Exclude<TranscriptWindowInput, { kind: "dispose" } | { hydration: Hydration }>,
  ): TranscriptWindowResult {
    switch (input.kind) {
      case "opening-failure":
      case "opening-success":
        return this.opening(input);
      case "replace-window":
        return this.replace(input.page);
      case "edge-visit":
        return this.visit(input);
      case "retry":
        return this.retry(input.direction);
      case "page-failure":
      case "page-success":
        return this.completePage(input);
      case "committed-row":
        this.state = admitRows(this.state, [input.row]);
        return { kind: "accepted", effects: [] };
      case "live-fact":
        this.state = project({ ...this.state, provisional: reduceLive(this.state.provisional, input.fact) });
        return { kind: "accepted", effects: [] };
      case "runtime-activity":
        return this.activity(input.activity);
      case "compaction-status":
        return this.compaction(input.status);
    }
  }

  private opening(
    input: Extract<TranscriptWindowInput, { kind: "opening-success" | "opening-failure" }>,
  ): TranscriptWindowResult {
    if (this.snapshot.opening.kind !== "loading" || input.permit !== this.openingPermit) {
      return { kind: "obsolete", effects: [] };
    }
    if (input.kind === "opening-success") return this.replace(input.page);
    this.state = {
      ...this.state,
      snapshot: { ...this.snapshot, opening: { kind: "error", error: input.error } },
    };
    return { kind: "accepted", effects: [{ kind: "opening-failed", error: input.error }] };
  }

  private replace(segment: Segment): TranscriptWindowResult {
    validateTail(segment);
    this.state = install(
      this.state,
      [shareSegment(segment, this.state.segments, this.state.pool)],
      "replace",
      segment.entries,
    );
    return { kind: "accepted", effects: [] };
  }

  private completePage(
    input: Extract<TranscriptWindowInput, { kind: "page-success" | "page-failure" }>,
  ): TranscriptWindowResult {
    if (!this.currentRequest(input.request)) return { kind: "obsolete", effects: [] };
    if (input.kind === "page-failure") {
      this.state = {
        ...this.state,
        pending: null,
        snapshot: {
          ...this.snapshot,
          [input.request.direction]: { kind: "error", cursor: input.request.cursor, error: input.error },
        },
      };
      return { kind: "accepted", effects: [] };
    }
    validateAdjacent(input.page, input.request);
    const segments = this.state.segments;
    const page = shareSegment(input.page, segments, this.state.pool);
    const neighbor = input.request.direction === "older" ? segments[0] : segments.at(-1);
    if (neighbor === undefined) throw new ContractError("Transcript edge request has no resident neighbor.");
    this.state = install(
      this.state,
      input.request.direction === "older" ? [page, neighbor] : [neighbor, page],
      "insert",
      input.page.entries,
    );
    return { kind: "accepted", effects: [] };
  }

  private retry(direction: TranscriptDirection): TranscriptWindowResult {
    const boundary = this.snapshot[direction];
    if (this.state.pending !== null || boundary.kind !== "error") return { kind: "accepted", effects: [] };
    return this.begin(direction, boundary.cursor);
  }

  private currentRequest(request: TranscriptPageRequest): boolean {
    const pending = this.state.pending?.request;
    if (pending?.admission !== request.admission) return false;
    if (pending.direction !== request.direction || pending.cursor !== request.cursor) {
      throw new ContractError("Transcript page outcome must match the admitted direction and cursor.");
    }
    return true;
  }

  private visit(input: Extract<TranscriptWindowInput, { kind: "edge-visit" }>): TranscriptWindowResult {
    if (this.state.pending !== null) return { kind: "accepted", effects: [] };
    const preferred = input.direction;
    const directions: readonly TranscriptDirection[] = [preferred, preferred === "older" ? "newer" : "older"];
    for (const direction of directions) {
      const boundary = this.snapshot[direction];
      if (input[direction] && boundary.kind === "idle" && boundary.cursor !== null) {
        return this.begin(direction, boundary.cursor);
      }
    }
    return { kind: "accepted", effects: [] };
  }

  private begin(direction: TranscriptDirection, cursor: number): TranscriptWindowResult {
    const request = { admission: Symbol("transcript edge"), direction, cursor };
    this.state = {
      ...this.state,
      pending: { request, previous: this.snapshot[direction] },
      snapshot: { ...this.snapshot, [direction]: { kind: "loading", cursor } },
    };
    return { kind: "accepted", effects: [{ kind: "page-request", request }] };
  }

  private hydrate(hydration: Hydration, reattachment: boolean): TranscriptWindowResult {
    const segment: Segment = {
      entries: hydration.TailSegment.Entries,
      olderCursor: hydration.TailSegment.OlderCursor,
      hasMoreAbove: hydration.TailSegment.HasMoreAbove,
      newerCursor: null,
      hasMoreBelow: false,
    };
    validateTail(segment);
    const checkpoint = hydration.SessionStatus.CompactionCount;
    if (!Number.isInteger(checkpoint) || checkpoint < 0) {
      throw new ContractError("Hydration completed compaction count must be nonnegative.");
    }
    if (reattachment && this.state.checkpoint !== null && checkpoint < this.state.checkpoint) {
      throw new ContractError("Reattachment completed compaction count regressed.");
    }
    const lifecycle = classifyHydration(hydration);
    const provisional = hydratedLive(hydration);
    if (reattachment && checkpoint === this.state.checkpoint) {
      const state = admitRows(
        {
          ...this.state,
          activity: hydration.RuntimeReadModelUpdate.Activity,
          provisional,
          lifecycle: null,
        },
        segment.entries,
      );
      this.state = { ...state, lifecycle };
      return { kind: "accepted", effects: [] };
    }
    const shared = shareSegment(segment, this.state.segments, this.state.pool);
    this.state = install(
      {
        ...this.state,
        pool: [],
        checkpoint,
        lifecycle,
        provisional,
        activity: hydration.RuntimeReadModelUpdate.Activity,
      },
      [shared],
      "replace",
      segment.entries,
    );
    return { kind: "accepted", effects: [] };
  }

  private activity(activity: RuntimeActivity): TranscriptWindowResult {
    const step = compactionStep(activity);
    const previous = this.state.lifecycle;
    if (activityContinues(previous, this.state.activity, activity)) {
      this.state = { ...this.state, activity };
      return { kind: "accepted", effects: [] };
    }
    const lifecycle = step === null ? null : beginStage(this.state.checkpoint, step);
    if (previous?.kind === "stage") {
      const state = admitRows({ ...this.state, lifecycle: null }, previous.rows);
      this.state = { ...state, activity, lifecycle };
    } else {
      this.state = { ...this.state, activity, lifecycle };
    }
    return { kind: "accepted", effects: [] };
  }

  private compaction(status: CompactionStatus): TranscriptWindowResult {
    let lifecycle = this.state.lifecycle;
    if (lifecycle === null) {
      if (status.State === "completed" && status.Count === this.state.checkpoint) {
        return { kind: "obsolete", effects: [] };
      }
      throw new ContractError("Compaction status has no active attempt.");
    }
    if (lifecycle.kind === "reflected") {
      if (status.State === "started" && status.Count === lifecycle.facts.Count + 1) {
        const step = this.state.activity === null ? null : compactionStep(this.state.activity);
        if (step === null) throw new ContractError("Compaction begin requires active Runtime Activity.");
        lifecycle = beginStage(this.state.checkpoint, step);
      } else {
        if (!sameCompaction(lifecycle.facts, status)) {
          throw new ContractError("Compaction status does not match the hydration-reflected completion.");
        }
        if (status.State === "completed") this.state = { ...this.state, lifecycle: null };
        return { kind: "accepted", effects: [] };
      }
    }
    const attempt = bindAttempt(lifecycle.attempt, status);
    if (status.State === "completed") return this.completeCompaction(status.Count);
    this.state = { ...this.state, lifecycle: { ...lifecycle, attempt } };
    return { kind: "accepted", effects: [] };
  }

  private completeCompaction(checkpoint: number): TranscriptWindowResult {
    const pending = this.state.pending;
    let snapshot = this.snapshot;
    if (this.state.provisional.length > 0) {
      snapshot = { ...snapshot, items: snapshot.items.filter((item) => "row" in item) };
    }
    if (pending !== null) snapshot = { ...snapshot, [pending.request.direction]: pending.previous };
    this.state = {
      ...this.state,
      checkpoint,
      lifecycle: null,
      pool: [],
      provisional: [],
      pending: null,
      snapshot,
    };
    return { kind: "accepted", effects: [{ kind: "scratch-rehydration" }] };
  }
}
