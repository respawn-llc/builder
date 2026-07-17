import { inboxNavNeighbors, orderedInboxTaskIDs } from "./inboxNavNeighbors";

describe("orderedInboxTaskIDs", () => {
  it("drops items without a task and de-duplicates while preserving order", () => {
    const items = [{ taskID: "a" }, { taskID: "" }, { taskID: "b" }, { taskID: "a" }, { taskID: "c" }];
    expect(orderedInboxTaskIDs(items)).toEqual(["a", "b", "c"]);
  });
});

describe("inboxNavNeighbors", () => {
  it("returns both siblings for a task in the middle", () => {
    expect(inboxNavNeighbors(["a", "b", "c"], "b", 1)).toEqual({
      previousTaskID: "a",
      nextTaskID: "c",
      anchorIndex: 1,
    });
  });

  it("has no previous for the first task", () => {
    expect(inboxNavNeighbors(["a", "b", "c"], "a", 0)).toEqual({
      previousTaskID: null,
      nextTaskID: "b",
      anchorIndex: 0,
    });
  });

  it("has no next for the last task", () => {
    expect(inboxNavNeighbors(["a", "b", "c"], "c", 2)).toEqual({
      previousTaskID: "b",
      nextTaskID: null,
      anchorIndex: 2,
    });
  });

  it("uses the last anchor so Next lands on the successor after the open task is resolved", () => {
    // "a" was at index 0 and has dropped out of the live inbox; "b" shifted up to 0.
    expect(inboxNavNeighbors(["b", "c"], "a", 0)).toEqual({
      previousTaskID: null,
      nextTaskID: "b",
      anchorIndex: 0,
    });
  });

  it("disables Next when the resolved task was last in the inbox", () => {
    // "c" was at index 2 in [x, y, c] and dropped out, leaving [x, y].
    expect(inboxNavNeighbors(["x", "y"], "c", 2)).toEqual({
      previousTaskID: "y",
      nextTaskID: null,
      anchorIndex: 2,
    });
  });

  it("yields no neighbors for an empty inbox", () => {
    expect(inboxNavNeighbors([], "a", 0)).toEqual({
      previousTaskID: null,
      nextTaskID: null,
      anchorIndex: 0,
    });
  });
});
