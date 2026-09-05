import { describe, expect, it } from "vitest";

import type { ChatTranscriptPayloadByKind } from "@/api";

import { TranscriptWindow } from "./index";
import { hydration, idle, page, row, sequences, visit } from "./fixtures";
const compacting = {
  ...idle,
  State: "running",
  ActiveStep: { RunID: "run", StepID: "step", ActiveKind: "compaction" },
} as const;
const activeCompaction: ChatTranscriptPayloadByKind["compaction_status"] = {
  StepID: "step",
  State: "started",
  Mode: "manual",
  Count: 1,
  RequestID: "request",
};
const started: ChatTranscriptPayloadByKind["compaction_status"] = {
  ...activeCompaction,
  Count: 0,
};
const completed: ChatTranscriptPayloadByKind["compaction_status"] = {
  ...activeCompaction,
  State: "completed",
};

describe("transcript live membership and compaction", () => {
  it("retains live rows while browsing and shares compatible page overlap without deleting omitted live rows", () => {
    const window = new TranscriptWindow();
    window.dispatch({ kind: "opening-success", permit: window.openingPermit, page: page([row(30)], 300) });
    const live = row(40);
    expect(window.dispatch({ kind: "committed-row", row: live }).kind).toBe("accepted");
    expect(sequences(window)).toEqual([30, 40]);
    window.dispatch({
      kind: "page-success",
      request: visit(window, "older"),
      page: page([row(20)], 200, 300),
    });
    window.dispatch({
      kind: "page-success",
      request: visit(window, "older"),
      page: page([row(10)], null, 200),
    });
    expect(sequences(window)).toEqual([10, 20, 40]);
    window.dispatch({
      kind: "live-fact",
      fact: {
        kind: "assistant_delta",
        payload: { StepID: "step", StreamID: "browse-stream", Phase: "commentary", Delta: "Draft" },
      },
    });
    expect(window.snapshot.items.at(-1)).toMatchObject({ kind: "assistant", state: "live" });
    const promoted = {
      ...row(50),
      Kind: "assistant",
      User: null,
      Assistant: {
        StepID: "step",
        StreamID: "browse-stream",
        Phase: "commentary",
        Text: "Committed",
        CondensedText: "",
        committed_at_unix_ms: null,
      },
    } as const;
    window.dispatch({ kind: "committed-row", row: promoted });
    expect(sequences(window)).toEqual([10, 20, 40, 50]);
    expect(window.snapshot.items.at(-1)).toMatchObject({ kind: "assistant", state: "committed" });
    window.dispatch({
      kind: "page-success",
      request: visit(window, "newer"),
      page: page([row(30), row(40)], 300),
    });
    expect(sequences(window)).toEqual([20, 30, 40, 50]);
    const overlap = window.snapshot.items.find(
      (item) => "row" in item && item.row.Locator.event_sequence === 40,
    );
    expect(overlap && "row" in overlap && overlap.row).toBe(live);
  });

  it("isolates staged rows until activity exits, then releases them through ordinary membership", () => {
    const window = new TranscriptWindow();
    const initial = hydration([row(30)], 0, compacting, activeCompaction);
    window.dispatch({
      kind: "initial-hydration",
      hydration: {
        ...initial,
        ActiveAssistant: { StepID: "step", StreamID: "stream", Phase: "commentary", Text: "Draft" },
      },
    });
    window.dispatch({
      kind: "live-fact",
      fact: {
        kind: "assistant_delta",
        payload: { StepID: "step", StreamID: "stream", Phase: "commentary", Delta: " response" },
      },
    });
    expect(window.snapshot.items.at(-1)).toMatchObject({
      kind: "assistant",
      state: "live",
      value: { Text: "Draft response" },
    });
    const committed = {
      ...row(40),
      Kind: "assistant",
      User: null,
      Assistant: { StepID: "step", StreamID: "stream", Phase: "commentary", Text: "Committed response" },
    } as const;
    expect(window.dispatch({ kind: "committed-row", row: committed }).kind).toBe("accepted");
    expect(sequences(window)).toEqual([30]);
    expect(window.snapshot.items.at(-1)).toMatchObject({ kind: "assistant", state: "live" });
    window.dispatch({ kind: "compaction-status", status: { ...started, State: "failed" } });
    expect(sequences(window)).toEqual([30]);
    const released = window.dispatch({ kind: "runtime-activity", activity: idle });
    expect(released).toEqual({ kind: "accepted", effects: [] });
    expect(sequences(window)).toEqual([30, 40]);
    expect(window.snapshot.items.at(-1)).toMatchObject({
      kind: "assistant",
      state: "committed",
      value: committed.Assistant,
    });
  });

  it("discards the visible pre-compaction pool and staged rows immediately on successful completion", () => {
    const window = new TranscriptWindow();
    window.dispatch({ kind: "initial-hydration", hydration: hydration([row(30)]) });
    window.dispatch({ kind: "committed-row", row: row(40) });
    expect(sequences(window)).toEqual([30, 40]);
    window.dispatch({ kind: "runtime-activity", activity: compacting });
    expect(window.dispatch({ kind: "compaction-status", status: started }).kind).toBe("accepted");
    window.dispatch({ kind: "committed-row", row: row(50) });
    expect(sequences(window)).toEqual([30, 40]);

    expect(window.dispatch({ kind: "compaction-status", status: completed }).effects).toEqual([
      { kind: "scratch-rehydration" },
    ]);
    expect(sequences(window)).toEqual([30]);
  });

  it("discards a successful stage and old pool, restores an executing edge's failure, and requests Scratch once", () => {
    const window = new TranscriptWindow();
    window.dispatch({ kind: "initial-hydration", hydration: hydration([row(30)]) });
    window.dispatch({ kind: "committed-row", row: row(40) });
    window.dispatch({
      kind: "page-success",
      request: visit(window, "older"),
      page: page([row(20)], 200, 300),
    });
    window.dispatch({
      kind: "page-success",
      request: visit(window, "older"),
      page: page([row(10)], 100, 200),
    });
    const failed = visit(window, "older");
    const error = new Error("Read failed");
    window.dispatch({ kind: "page-failure", request: failed, error });
    const retry = window.dispatch({ kind: "retry", direction: "older" }).effects[0];
    if (retry?.kind !== "page-request") throw new Error("Expected Retry.");
    window.dispatch({ kind: "runtime-activity", activity: compacting });
    window.dispatch({ kind: "compaction-status", status: started });
    window.dispatch({ kind: "committed-row", row: row(50) });
    const complete = { kind: "compaction-status", status: completed } as const;
    expect(window.dispatch(complete)).toEqual({
      kind: "accepted",
      effects: [{ kind: "scratch-rehydration" }],
    });
    expect(sequences(window)).toEqual([10, 20]);
    expect(window.snapshot.older).toEqual({ kind: "error", cursor: 100, error });
    expect(window.dispatch(complete).effects).toEqual([]);
    expect(window.dispatch({ kind: "page-failure", request: retry.request, error }).kind).toBe("obsolete");
    window.dispatch({ kind: "runtime-activity", activity: idle });
    window.dispatch({ kind: "page-success", request: visit(window, "newer"), page: page([row(60)], 300) });
    expect(sequences(window)).toEqual([20, 60]);
    expect(
      window.dispatch({ kind: "scratch-hydration", hydration: hydration([row(60)], 1) }).effects,
    ).toEqual([]);
    expect(sequences(window)).toEqual([60]);
  });

  it("settles a global-tail page during staging with unchanged page edges through success until hydration", () => {
    const window = new TranscriptWindow();
    window.dispatch({ kind: "initial-hydration", hydration: hydration([row(30)]) });
    window.dispatch({ kind: "committed-row", row: row(40) });
    window.dispatch({
      kind: "page-success",
      request: visit(window, "older"),
      page: page([row(20)], 200, 300),
    });
    window.dispatch({
      kind: "page-success",
      request: visit(window, "older"),
      page: page([row(10)], 100, 200),
    });
    window.dispatch({ kind: "runtime-activity", activity: compacting });
    window.dispatch({ kind: "compaction-status", status: started });
    window.dispatch({ kind: "committed-row", row: row(50) });
    const result = window.dispatch({
      kind: "page-success",
      request: visit(window, "newer"),
      page: page([row(60)], 300),
    });
    expect(result).toEqual({ kind: "accepted", effects: [] });
    expect(sequences(window)).toEqual([20, 40, 60]);
    const settled = window.snapshot;
    expect(
      window.dispatch({
        kind: "compaction-status",
        status: completed,
      }).effects,
    ).toEqual([{ kind: "scratch-rehydration" }]);
    expect(sequences(window)).toEqual([20, 60]);
    expect(window.snapshot.older).toBe(settled.older);
    expect(window.snapshot.newer).toBe(settled.newer);
    const replacement = hydration([row(70)], 1);
    window.dispatch({
      kind: "reattachment-hydration",
      hydration: {
        ...replacement,
        TailSegment: { Entries: [row(70)], HasMoreAbove: true, OlderCursor: 700 },
      },
    });
    // The local completion is already reflected: equal-count reattachment reconciles, not replaces.
    expect(sequences(window)).toEqual([20, 60, 70]);
    expect(window.snapshot.older).toBe(settled.older);
    expect(window.snapshot.newer).toBe(settled.newer);
    window.dispatch({
      kind: "reattachment-hydration",
      hydration: {
        ...replacement,
        SessionStatus: { ...replacement.SessionStatus, CompactionCount: 2 },
        TailSegment: { Entries: [row(70)], HasMoreAbove: true, OlderCursor: 700 },
      },
    });
    expect(sequences(window)).toEqual([70]);
    expect(window.snapshot.older.cursor).toBe(700);
  });

  it("classifies a hydration-reflected completion and keeps its marker inert across activity clear", () => {
    const window = new TranscriptWindow();
    expect(
      window.dispatch({
        kind: "initial-hydration",
        hydration: hydration([row(30)], 1, compacting, activeCompaction),
      }).kind,
    ).toBe("accepted");
    window.dispatch({ kind: "runtime-activity", activity: idle });
    const settled = window.snapshot;
    expect(
      window.dispatch({
        kind: "compaction-status",
        status: completed,
      }),
    ).toEqual({ kind: "accepted", effects: [] });
    expect(window.snapshot).toBe(settled);
    expect(sequences(window)).toEqual([30]);
  });
});
