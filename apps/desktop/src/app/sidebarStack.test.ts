import type { SidebarDestination } from "@/app-facade";
import {
  createSidebarStack,
  sidebarStackReducer,
  type SidebarStackAction,
  type SidebarStackEntry,
} from "./sidebarStack";

const task = (taskID: string): SidebarDestination => ({
  kind: "taskDetail",
  taskID,
});

const newTask: SidebarDestination = {
  kind: "newTask",
  projectID: "project-1",
  workflowID: "workflow-1",
  boardQueryWorkflowID: undefined,
};

function apply(state: ReturnType<typeof createSidebarStack> | null, action: SidebarStackAction) {
  return sidebarStackReducer(state, action);
}

function entryIDs(entries: readonly SidebarStackEntry[]) {
  return entries.map((entry) => entry.entryID);
}

describe("sidebarStackReducer", () => {
  it("creates a root entry with the supplied lifecycle and entry IDs", () => {
    const state = apply(null, {
      type: "open",
      activationID: "activation-1",
      lifecycleID: "lifecycle-1",
      entryID: "entry-1",
      destination: task("task-1"),
    });

    expect(state).toEqual({
      activationID: "activation-1",
      lifecycleID: "lifecycle-1",
      entries: [
        {
          entryID: "entry-1",
          destination: task("task-1"),
        },
      ],
    });
  });

  it("replaces only the current entry with a new entry ID", () => {
    const opened = createSidebarStack("lifecycle-1", {
      entryID: "entry-1",
      destination: task("task-1"),
    });

    const replaced = apply(opened, {
      type: "replace",
      entryID: "entry-2",
      destination: task("task-2"),
    });

    expect(replaced?.lifecycleID).toBe("lifecycle-1");
    expect(replaced?.entries).toEqual([{ entryID: "entry-2", destination: task("task-2") }]);
  });

  it("preserves preceding entries when replacing the current destination", () => {
    const opened = createSidebarStack("lifecycle-1", {
      entryID: "entry-1",
      destination: task("task-1"),
    });
    const pushed = apply(opened, {
      type: "push",
      entryID: "entry-2",
      destination: newTask,
    });
    const replaced = apply(pushed, {
      type: "replace",
      entryID: "entry-3",
      destination: task("task-3"),
    });

    expect(entryIDs(replaced?.entries ?? [])).toEqual(["entry-1", "entry-3"]);
  });

  it("returns to a retained Task Detail when replacement targets an earlier entry", () => {
    const opened = createSidebarStack("lifecycle-1", {
      entryID: "entry-1",
      destination: task("task-1"),
    });
    const pushed = apply(opened, {
      type: "push",
      entryID: "entry-2",
      destination: task("task-2"),
    });
    const replaced = apply(pushed, {
      type: "replace",
      activationID: "activation-3",
      entryID: "entry-3",
      destination: task("task-1"),
    });

    expect(entryIDs(replaced?.entries ?? [])).toEqual(["entry-1"]);
    expect(replaced?.entries[0]?.entryID).toBe("entry-1");
    expect(replaced?.activationID).toBe("activation-3");
  });

  it("ignores stale directional actions after the current token changes", () => {
    const state = createSidebarStack("lifecycle-1", {
      entryID: "entry-1",
      destination: task("task-1"),
    });
    const pushed = apply(state, {
      type: "push",
      lifecycleID: "lifecycle-1",
      sourceEntryID: "entry-1",
      entryID: "entry-2",
      destination: task("task-2"),
    });
    const replaced = apply(pushed, {
      type: "replace",
      activationID: "activation-3",
      entryID: "entry-3",
      destination: task("task-3"),
    });
    expect(
      apply(replaced, {
        type: "back",
        activationID: "activation-stale-back",
        lifecycleID: "lifecycle-1",
        entryID: "entry-2",
      }),
    ).toBe(replaced);
    expect(
      apply(replaced, {
        type: "push",
        activationID: "activation-stale-push",
        lifecycleID: "lifecycle-1",
        sourceEntryID: "entry-2",
        entryID: "entry-4",
        destination: task("task-4"),
      }),
    ).toBe(replaced);
    expect(replaced?.activationID).toBe("activation-3");
  });

  it("pushes a destination and Back removes the current entry", () => {
    const opened = createSidebarStack("lifecycle-1", {
      entryID: "entry-1",
      destination: task("task-1"),
    });
    const pushed = apply(opened, {
      type: "push",
      activationID: "activation-2",
      entryID: "entry-2",
      destination: task("task-2"),
    });

    expect(entryIDs(pushed?.entries ?? [])).toEqual(["entry-1", "entry-2"]);
    expect(pushed?.entries.at(-1)?.destination).toEqual(task("task-2"));

    const backed = apply(pushed, {
      type: "back",
      activationID: "activation-3",
      lifecycleID: "lifecycle-1",
      entryID: "entry-2",
    });
    expect(entryIDs(backed?.entries ?? [])).toEqual(["entry-1"]);
    expect(backed?.activationID).toBe("activation-3");
  });

  it("truncates to an earlier Task Detail instead of creating a duplicate", () => {
    let state: ReturnType<typeof createSidebarStack> | null = createSidebarStack("lifecycle-1", {
      entryID: "entry-1",
      destination: task("task-1"),
    });
    state = apply(state, {
      type: "push",
      entryID: "entry-2",
      destination: task("task-2"),
    });
    state = apply(state, {
      type: "push",
      entryID: "entry-3",
      destination: task("task-3"),
    });

    const returned = apply(state, {
      type: "push",
      entryID: "entry-4",
      destination: task("task-1"),
    });

    expect(entryIDs(returned?.entries ?? [])).toEqual(["entry-1"]);
    expect(returned?.entries[0]?.destination).toEqual(task("task-1"));
  });

  it("preserves the root and evicts the oldest non-root entry at capacity", () => {
    let state: ReturnType<typeof createSidebarStack> | null = createSidebarStack("lifecycle-1", {
      entryID: "entry-0",
      destination: task("task-0"),
    });

    for (let index = 1; index < 50; index += 1) {
      state = apply(state, {
        type: "push",
        entryID: `entry-${index.toString()}`,
        destination: task(`task-${index.toString()}`),
      });
    }
    if (state === null) {
      throw new Error("Stack unexpectedly closed.");
    }
    expect(state.entries).toHaveLength(50);

    const bounded = apply(state, {
      type: "push",
      entryID: "entry-50",
      destination: task("task-50"),
    });

    expect(bounded?.entries).toHaveLength(50);
    expect(bounded?.entries[0]).toEqual(state.entries[0]);
    expect(bounded?.entries[1]?.destination).toEqual(task("task-2"));
    expect(bounded?.entries.at(-1)?.destination).toEqual(task("task-50"));
  });

  it("removes exactly the matching entry and skips stale or unknown tokens", () => {
    let state: ReturnType<typeof createSidebarStack> | null = createSidebarStack("lifecycle-1", {
      entryID: "entry-1",
      destination: task("task-1"),
    });
    state = apply(state, {
      type: "push",
      entryID: "entry-2",
      destination: newTask,
    });
    if (state === null) {
      throw new Error("Stack unexpectedly closed.");
    }

    expect(
      apply(state, {
        type: "remove",
        lifecycleID: "lifecycle-2",
        entryID: "entry-2",
      }),
    ).toBe(state);
    expect(
      apply(state, {
        type: "remove",
        lifecycleID: "lifecycle-1",
        entryID: "entry-stale",
      }),
    ).toBe(state);

    const removed = apply(state, {
      type: "remove",
      lifecycleID: "lifecycle-1",
      entryID: "entry-2",
    });
    expect(entryIDs(removed?.entries ?? [])).toEqual(["entry-1"]);

    const final = apply(removed, {
      type: "remove",
      lifecycleID: "lifecycle-1",
      entryID: "entry-1",
    });
    expect(final).toBeNull();
  });

  it("ignores actions for a different lifecycle and closes the complete stack", () => {
    const state = createSidebarStack("lifecycle-1", {
      entryID: "entry-1",
      destination: task("task-1"),
    });

    expect(
      apply(state, {
        type: "push",
        entryID: "entry-2",
        destination: task("task-2"),
        lifecycleID: "lifecycle-2",
      }),
    ).toBe(state);
    expect(apply(state, { type: "close", lifecycleID: "lifecycle-2" })).toBe(state);
    expect(apply(state, { type: "close", lifecycleID: "lifecycle-1" })).toBeNull();
  });
});
