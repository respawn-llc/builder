import { describe, expect, it } from "vitest";

import { canonicalBoardFilter } from "@/api";
import type { BoardFilter, TaskLabelFilter, WorkflowBoard } from "@/api";
import { createBoardFilterGenerationController } from "./BoardFilterGenerationController";

const priorityID = "11111111-1111-4111-8111-111111111111";
const urgentID = "22222222-2222-4222-8222-222222222222";
const smallID = "33333333-3333-4333-8333-333333333333";

describe("BoardFilterGenerationController", () => {
  it("promotes a desired filter immediately while the active generation is idle", () => {
    const controller = createBoardFilterGenerationController({ kind: "none" });
    const priority = namedFilter("any", priorityID);

    controller.setDesiredFilter(priority);

    expect(controller.getSnapshot()).toMatchObject({
      active: {
        generation: 2,
        filter: canonicalBoardFilter(priority),
        retiring: false,
      },
      desiredFilter: null,
    });
  });

  it("promotes when the excluded partition changes", () => {
    const controller = createBoardFilterGenerationController({
      kind: "named",
      mode: "any",
      labelIDs: [priorityID],
      excludedLabelIDs: [urgentID],
    });
    const desired = {
      kind: "named" as const,
      mode: "any" as const,
      labelIDs: [priorityID],
      excludedLabelIDs: [smallID],
    };

    controller.setDesiredFilter(desired);

    expect(controller.getSnapshot()).toMatchObject({
      active: {
        generation: 2,
        filter: canonicalBoardFilter(desired),
        retiring: false,
      },
      desiredFilter: null,
    });
  });

  it("retains only the latest desired filter while an active transport lease is unresolved", async () => {
    const promoted: BoardFilter[] = [];
    const controller = createBoardFilterGenerationController(
      { kind: "none" },
      {
        onPromoted: (generation) => {
          promoted.push(generation.filter);
        },
      },
    );
    let settle: ((board: WorkflowBoard) => void) | undefined;
    const unresolved = new Promise<WorkflowBoard>((resolve) => {
      settle = resolve;
    });
    const activeRequest = admittedPromise(
      controller.admitBoardTransport(1, "board:project-1:workflow-1", async () => unresolved),
    );

    const priority = namedFilter("any", priorityID);
    const priorityAndUrgent = namedFilter("all", priorityID, urgentID);
    controller.setDesiredFilter(priority);
    controller.setDesiredFilter({ kind: "unlabeled" });
    controller.setDesiredFilter(priorityAndUrgent);

    expect(controller.getSnapshot()).toMatchObject({
      active: {
        generation: 1,
        filter: canonicalBoardFilter({ kind: "none" }),
        retiring: true,
      },
      desiredFilter: canonicalBoardFilter(priorityAndUrgent),
    });
    expect(promoted).toEqual([]);

    settle?.(testBoard());
    await activeRequest;

    expect(controller.getSnapshot()).toMatchObject({
      active: {
        generation: 2,
        filter: canonicalBoardFilter(priorityAndUrgent),
        retiring: false,
      },
      desiredFilter: null,
    });
    expect(promoted).toEqual([canonicalBoardFilter(priorityAndUrgent)]);
  });

  it("keeps a generation active until its registered TanStack orchestration settles", async () => {
    const controller = createBoardFilterGenerationController({ kind: "none" });
    let settle: (() => void) | undefined;
    const orchestration = new Promise<void>((resolve) => {
      settle = resolve;
    });
    controller.registerOrchestration(1, "board:project-1:workflow-1", orchestration);

    controller.setDesiredFilter({ kind: "unlabeled" });

    expect(controller.getSnapshot().active).toMatchObject({
      generation: 1,
      retiring: true,
    });
    settle?.();
    await orchestration;
    await Promise.resolve();
    expect(controller.getSnapshot().active).toMatchObject({
      generation: 2,
      filter: canonicalBoardFilter({ kind: "unlabeled" }),
      retiring: false,
    });
  });

  it("waits for the generation cancellation barrier after issued transports settle", async () => {
    let settleCancellation: (() => void) | undefined;
    const cancellation = new Promise<void>((resolve) => {
      settleCancellation = resolve;
    });
    const controller = createBoardFilterGenerationController(
      { kind: "none" },
      {
        onRetiring: async () => {
          await cancellation;
        },
      },
    );
    let settleTransport: ((board: WorkflowBoard) => void) | undefined;
    const transport = admittedPromise(
      controller.admitBoardTransport(
        1,
        "board:project-1:workflow-1",
        async () =>
          new Promise<WorkflowBoard>((resolve) => {
            settleTransport = resolve;
          }),
      ),
    );

    controller.setDesiredFilter({ kind: "unlabeled" });
    settleTransport?.(testBoard());
    await transport;
    await Promise.resolve();

    expect(controller.getSnapshot().active.generation).toBe(1);
    settleCancellation?.();
    await cancellation;
    await waitUntil(() => controller.getSnapshot().active.generation === 2);
    expect(controller.getSnapshot().active.generation).toBe(2);
  });

  it("denies a stale generation without invoking its transport", () => {
    const controller = createBoardFilterGenerationController({ kind: "none" });
    controller.setDesiredFilter({ kind: "unlabeled" });
    let invoked = false;

    const admission = controller.admitBoardTransport(1, "board:stale", async () => {
      invoked = true;
      return testBoard();
    });

    expect(admission).toEqual({ kind: "denied" });
    expect(invoked).toBe(false);
  });

  it("tracks a closure-race cancellation barrier added after retirement begins", async () => {
    const controller = createBoardFilterGenerationController({ kind: "none" });
    let settleTransport: ((board: WorkflowBoard) => void) | undefined;
    const transport = admittedPromise(
      controller.admitBoardTransport(
        1,
        "board:project-1:workflow-1",
        async () =>
          new Promise<WorkflowBoard>((resolve) => {
            settleTransport = resolve;
          }),
      ),
    );
    controller.setDesiredFilter({ kind: "unlabeled" });
    let settleRaceCancellation: (() => void) | undefined;
    const raceCancellation = new Promise<void>((resolve) => {
      settleRaceCancellation = resolve;
    });

    controller.registerCancellationBarrier(1, raceCancellation);
    settleTransport?.(testBoard());
    await transport;
    await Promise.resolve();

    expect(controller.getSnapshot().active.generation).toBe(1);
    settleRaceCancellation?.();
    await raceCancellation;
    await Promise.resolve();
    expect(controller.getSnapshot().active.generation).toBe(2);
  });

  it("joins an exact unresolved transport operation across owner reactivation", async () => {
    const controller = createBoardFilterGenerationController({ kind: "none" });
    let settle: ((board: WorkflowBoard) => void) | undefined;
    let transportCalls = 0;
    const first = admittedPromise(
      controller.admitBoardTransport(1, "cards:node-1:first", async () => {
        transportCalls += 1;
        return new Promise<WorkflowBoard>((resolve) => {
          settle = resolve;
        });
      }),
    );
    const reactivated = admittedPromise(
      controller.admitBoardTransport(1, "cards:node-1:first", async () => {
        transportCalls += 1;
        return testBoard("duplicate");
      }),
    );

    expect(transportCalls).toBe(1);
    const authoritative = testBoard("authoritative");
    settle?.(authoritative);
    await expect(Promise.all([first, reactivated])).resolves.toEqual([authoritative, authoritative]);
  });

  it("coalesces one hundred immediate edits into the latest desired filter", async () => {
    const promoted: BoardFilter[] = [];
    const controller = createBoardFilterGenerationController(
      { kind: "none" },
      {
        onPromoted: (generation) => {
          promoted.push(generation.filter);
        },
      },
    );
    let settle: ((board: WorkflowBoard) => void) | undefined;
    const active = admittedPromise(
      controller.admitBoardTransport(
        1,
        "board:active",
        async () =>
          new Promise<WorkflowBoard>((resolve) => {
            settle = resolve;
          }),
      ),
    );

    for (let index = 1; index <= 100; index += 1) {
      controller.setDesiredFilter(namedFilter(index % 2 === 0 ? "all" : "any", labelID(index)));
    }

    expect(controller.getSnapshot().desiredFilter).toEqual(
      canonicalBoardFilter(namedFilter("all", labelID(100))),
    );
    expect(promoted).toEqual([]);
    settle?.(testBoard());
    await active;
    await Promise.resolve();
    expect(promoted).toEqual([canonicalBoardFilter(namedFilter("all", labelID(100)))]);
  });

  it("waits for unresolved newer and older page leases independently", async () => {
    const controller = createBoardFilterGenerationController({ kind: "none" });
    let settleNewer: ((board: WorkflowBoard) => void) | undefined;
    let settleOlder: ((board: WorkflowBoard) => void) | undefined;
    const newer = admittedPromise(
      controller.admitBoardTransport(
        1,
        "cards:node-1:newer",
        async () =>
          new Promise<WorkflowBoard>((resolve) => {
            settleNewer = resolve;
          }),
      ),
    );
    const older = admittedPromise(
      controller.admitBoardTransport(
        1,
        "cards:node-1:older",
        async () =>
          new Promise<WorkflowBoard>((resolve) => {
            settleOlder = resolve;
          }),
      ),
    );

    controller.setDesiredFilter({ kind: "unlabeled" });
    settleNewer?.(testBoard());
    await newer;
    await Promise.resolve();
    expect(controller.getSnapshot().active.generation).toBe(1);

    settleOlder?.(testBoard());
    await older;
    await Promise.resolve();
    expect(controller.getSnapshot().active.generation).toBe(2);
  });
});

function namedFilter(mode: "any" | "all", ...labelIDs: readonly string[]): TaskLabelFilter {
  return {
    kind: "named",
    mode,
    labelIDs: [...labelIDs].sort(),
    excludedLabelIDs: [],
  };
}

async function admittedPromise<T>(
  admission: Readonly<{ kind: "admitted"; promise: Promise<T> }> | Readonly<{ kind: "denied" }>,
): Promise<T> {
  if (admission.kind === "denied") {
    throw new Error("Expected board transport admission.");
  }
  return admission.promise;
}

function testBoard(projectName = "Project"): WorkflowBoard {
  return {
    attachedWorkspaceCount: 0,
    columns: [],
    defaultWorkspaceID: "workspace-1",
    generatedAt: 1,
    groups: [],
    projectID: "project-1",
    projectKey: "PRO",
    projectName,
    selectedWorkflow: null,
    workflows: [],
  };
}

function labelID(index: number): string {
  return `00000000-0000-4000-8000-${index.toString().padStart(12, "0")}`;
}

async function waitUntil(predicate: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    if (predicate()) {
      return;
    }
    await Promise.resolve();
  }
  throw new Error("Condition did not become true.");
}
