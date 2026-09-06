import { describe, expect, it } from "vitest";

import type { ChatTranscriptPage, ChatTranscriptPayloadByKind } from "@/api";

import { TranscriptWindow } from "./index";
import { hydration, page, row, sequences, visit } from "./fixtures";

function open(window: TranscriptWindow, tail: ChatTranscriptPage): void {
  expect(window.dispatch({ kind: "opening-success", permit: window.openingPermit, page: tail }).kind).toBe(
    "accepted",
  );
}

describe("bounded transcript window", () => {
  it("opens one admitted tail once, retaining source rows and opaque edges", () => {
    const window = new TranscriptWindow();
    expect(window.snapshot.items).toEqual([]);
    expect(window.snapshot.opening.kind).toBe("loading");
    const source = row(30);
    const input = {
      kind: "opening-success",
      permit: window.openingPermit,
      page: page([source], 987),
    } as const;

    expect(window.dispatch(input).kind).toBe("accepted");
    expect(window.snapshot.items.flatMap((item) => ("row" in item ? [item.row] : []))).toEqual([source]);
    const first = window.snapshot.items[0];
    expect(first && "row" in first && first.row).toBe(source);
    expect(window.snapshot.items[0]).toMatchObject({
      state: "committed",
      kind: "user",
      value: source.User,
    });
    expect(window.snapshot.older).toEqual({ kind: "idle", cursor: 987 });
    expect(window.snapshot.newer).toEqual({ kind: "idle", cursor: null });
    expect(window.snapshot.opening.kind).toBe("ready");
    const settled = window.snapshot;
    expect(window.dispatch(input).kind).toBe("obsolete");
    expect(window.snapshot).toBe(settled);
  });

  it("moves across neighboring opaque cursors and evicts only the opposite nonadjacent segment", () => {
    const window = new TranscriptWindow();
    const tail = page([row(20), row(30)], 987);
    const middle = page([row(20)], 123, 987);
    const oldest = page([row(10)], null, 123);
    open(window, tail);

    const older = visit(window, "older");
    expect(older).toMatchObject({ direction: "older", cursor: 987 });
    expect(window.snapshot.older).toMatchObject({ kind: "loading", cursor: 987 });
    expect(window.dispatch({ kind: "page-success", request: { ...older }, page: middle }).kind).toBe(
      "accepted",
    );
    expect(sequences(window)).toEqual([20, 30]);
    expect(window.dispatch({ kind: "page-success", request: older, page: middle }).kind).toBe("obsolete");

    const nextOlder = visit(window, "older");
    expect(nextOlder.cursor).toBe(123);
    window.dispatch({ kind: "page-success", request: nextOlder, page: oldest });
    expect(sequences(window)).toEqual([10, 20]);
    expect(window.snapshot.older.cursor).toBeNull();
    expect(window.snapshot.newer.cursor).toBe(987);

    const newer = visit(window, "newer");
    expect(newer).toMatchObject({ direction: "newer", cursor: 987 });
    window.dispatch({ kind: "page-success", request: newer, page: tail });
    expect(sequences(window)).toEqual([20, 30]);
    expect(window.snapshot.older.cursor).toBe(123);
    expect(window.snapshot.newer.cursor).toBeNull();
  });

  it("keeps empty and hidden-only shells navigable without rendering hidden rows", () => {
    const window = new TranscriptWindow();
    open(window, page([], 987));
    const hidden = { ...row(20), Visibility: "hidden" } as const;
    window.dispatch({
      kind: "page-success",
      request: visit(window, "older"),
      page: page([hidden], 123, 987),
    });
    expect(window.snapshot.items).toEqual([]);
    expect(window.snapshot.older.cursor).toBe(123);
    window.dispatch({ kind: "page-success", request: visit(window, "older"), page: page([], 456, 123) });
    expect(window.snapshot.items).toEqual([]);
    expect(window.snapshot.older.cursor).toBe(456);
    expect(window.snapshot.newer.cursor).toBe(987);
    window.dispatch({ kind: "page-success", request: visit(window, "newer"), page: page([], 987) });
    expect(window.snapshot.older.cursor).toBe(123);
    expect(window.snapshot.newer.cursor).toBeNull();
  });

  it("uses deliberate direction for competing edges, loads one edge, and retries only the failed cursor", () => {
    const window = new TranscriptWindow();
    open(window, page([row(40)], 400));
    window.dispatch({
      kind: "page-success",
      request: visit(window, "older"),
      page: page([row(30)], 300, 400),
    });
    window.dispatch({
      kind: "page-success",
      request: visit(window, "older"),
      page: page([row(20)], 200, 300),
    });
    const newer = visit(window, "newer");
    expect(newer).toMatchObject({ direction: "newer", cursor: 400 });
    expect(
      window.dispatch({ kind: "edge-visit", direction: "older", older: true, newer: true }).effects,
    ).toEqual([]);
    expect(window.snapshot.older.kind).toBe("idle");
    const error = new Error("Page read failed");
    window.dispatch({ kind: "page-failure", request: newer, error });
    expect(sequences(window)).toEqual([20, 30]);
    expect(window.snapshot.newer).toEqual({ kind: "error", cursor: 400, error });
    expect(
      window.dispatch({ kind: "edge-visit", direction: "newer", older: false, newer: true }).effects,
    ).toEqual([]);

    const retry = window.dispatch({ kind: "retry", direction: "newer" }).effects[0];
    if (retry?.kind !== "page-request") throw new Error("Expected exact-cursor Retry.");
    expect(retry.request).toMatchObject({ direction: "newer", cursor: 400 });
    expect(retry.request.admission).not.toBe(newer.admission);
    expect(window.dispatch({ kind: "retry", direction: "newer" }).effects).toEqual([]);
    expect(
      window.dispatch({ kind: "page-success", request: retry.request, page: page([row(40)], 400) }).effects,
    ).toEqual([]);
    expect(sequences(window)).toEqual([30, 40]);
    expect(window.snapshot.newer).toEqual({ kind: "idle", cursor: null });
    expect(window.snapshot.older.kind).toBe("idle");
    const older = visit(window, "older");
    expect(older).toMatchObject({ direction: "older", cursor: 300 });
  });

  it("rejects one malformed bounded batch atomically without settling the current edge", () => {
    const window = new TranscriptWindow();
    open(window, page([row(30)], 987));
    const request = visit(window, "older");
    const before = window.snapshot;
    const duplicate = row(20);
    const result = window.dispatch({
      kind: "page-success",
      request,
      page: page([duplicate, duplicate], 123, 987),
    });
    expect(result.kind).toBe("contract-failure");
    expect(result.effects).toEqual([]);
    expect(window.snapshot).toBe(before);
    expect(window.dispatch({ kind: "page-success", request, page: page([duplicate], 123, 987) }).kind).toBe(
      "accepted",
    );
    expect(sequences(window)).toEqual([20, 30]);
  });

  it("replaces the window, clears exact failures, and obsoletes both outcomes of prior edge work", () => {
    for (const outcome of ["success", "failure"] as const) {
      const window = new TranscriptWindow();
      open(window, page([row(40)], 400));
      window.dispatch({
        kind: "page-success",
        request: visit(window, "older"),
        page: page([row(30)], 300, 400),
      });
      window.dispatch({
        kind: "page-success",
        request: visit(window, "older"),
        page: page([row(20)], 200, 300),
      });
      const failed = visit(window, "older");
      window.dispatch({ kind: "page-failure", request: failed, error: new Error("Edge failed") });
      window.dispatch({
        kind: "page-failure",
        request: visit(window, "newer"),
        error: new Error("Other edge failed"),
      });
      const retry = window.dispatch({ kind: "retry", direction: "newer" }).effects[0];
      if (retry?.kind !== "page-request") throw new Error("Expected exact-cursor Retry.");
      expect(window.snapshot.older.kind).toBe("error");
      expect(window.snapshot.newer.kind).toBe("loading");
      expect(window.dispatch({ kind: "replace-window", page: page([row(50)], 200) }).effects).toEqual([]);
      expect(sequences(window)).toEqual([50]);
      expect(window.snapshot.older).toEqual({ kind: "idle", cursor: 200 });
      expect(window.snapshot.newer).toEqual({ kind: "idle", cursor: null });
      const replacement = window.snapshot;

      const result =
        outcome === "success"
          ? window.dispatch({ kind: "page-success", request: retry.request, page: page([row(40)], 400) })
          : window.dispatch({
              kind: "page-failure",
              request: retry.request,
              error: new Error("Old edge failed"),
            });
      expect(result.kind).toBe("obsolete");
      expect(result.effects).toEqual([]);
      expect(window.snapshot).toBe(replacement);
      expect(window.dispatch({ kind: "retry", direction: "older" }).effects).toEqual([]);
      const current = visit(window, "older");
      expect(current.cursor).toBe(200);
      expect(current.admission).not.toBe(retry.request.admission);
    }
  });

  it("installs an initial hydration tail and closes the outstanding opening permit", () => {
    const window = new TranscriptWindow();
    const tail: ChatTranscriptPayloadByKind["hydration"]["TailSegment"] = {
      Entries: [row(30)],
      HasMoreAbove: true,
      OlderCursor: 987,
    };
    expect(
      window.dispatch({
        kind: "initial-hydration",
        hydration: { ...hydration(tail.Entries), TailSegment: tail },
      }).kind,
    ).toBe("accepted");
    expect(sequences(window)).toEqual([30]);
    expect(window.snapshot.older).toEqual({ kind: "idle", cursor: 987 });
    expect(window.snapshot.newer).toEqual({ kind: "idle", cursor: null });
    const hydrated = window.snapshot;
    expect(
      window.dispatch({ kind: "opening-success", permit: window.openingPermit, page: page([row(10)], null) })
        .kind,
    ).toBe("obsolete");
    expect(window.snapshot).toBe(hydrated);
  });

  it("reports the complete-open failure without partial membership and disposes the mounted owner", () => {
    const window = new TranscriptWindow();
    const error = new Error("Session open failed");
    const failure = { kind: "opening-failure", permit: window.openingPermit, error } as const;
    expect(window.dispatch(failure)).toEqual({
      kind: "accepted",
      effects: [{ kind: "opening-failed", error }],
    });
    expect(window.snapshot.opening).toEqual({ kind: "error", error });
    expect(window.snapshot.items).toEqual([]);
    expect(window.dispatch(failure).kind).toBe("obsolete");
    expect(window.dispatch({ kind: "retry", direction: "older" }).effects).toEqual([]);
    expect(window.dispatch({ kind: "dispose" }).kind).toBe("disposed");
    const disposed = window.snapshot;
    expect(
      window.dispatch({ kind: "opening-success", permit: window.openingPermit, page: page([row(30)], 987) })
        .kind,
    ).toBe("disposed");
    expect(window.snapshot).toBe(disposed);

    const mounted = new TranscriptWindow();
    open(mounted, page([row(30)], 987));
    const request = visit(mounted, "older");
    mounted.dispatch({ kind: "dispose" });
    expect(mounted.snapshot.items).toEqual([]);
    expect(mounted.snapshot.opening.kind).toBe("disposed");
    expect(mounted.dispatch({ kind: "page-success", request, page: page([row(20)], 123, 987) }).kind).toBe(
      "disposed",
    );
    expect(mounted.dispatch({ kind: "replace-window", page: page([row(40)], 456) }).kind).toBe("disposed");
    expect(mounted.dispatch({ kind: "dispose" }).kind).toBe("disposed");
  });
});
