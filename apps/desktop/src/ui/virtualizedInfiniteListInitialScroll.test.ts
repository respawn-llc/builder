import {
  resolveVirtualizedInitialScroll,
  virtualizedInitialScrollIndex,
} from "./virtualizedInfiniteListInitialScroll";

describe("virtualized initial scroll", () => {
  it("waits for the keyed item and resolves it without changing focus", () => {
    const getItemKey = (item: Readonly<{ key: string }>) => item.key;
    expect(
      resolveVirtualizedInitialScroll({
        getItemKey,
        headerCount: 0,
        initialScrollKey: "dependencies",
        initialScrollRequestKey: "task-1:dependencies",
        items: [],
        lastRequestKey: null,
      }),
    ).toBeNull();
    expect(
      resolveVirtualizedInitialScroll({
        getItemKey,
        headerCount: 0,
        initialScrollKey: "dependencies",
        initialScrollRequestKey: "task-1:dependencies",
        items: [{ key: "header" }, { key: "dependencies" }],
        lastRequestKey: null,
      }),
    ).toEqual({ requestKey: "task-1:dependencies", scrollIndex: 1 });
    expect(
      virtualizedInitialScrollIndex({
        getItemKey,
        headerCount: 0,
        initialScrollKey: "dependencies",
        items: [{ key: "dependencies" }],
      }),
    ).toBe(0);
  });
});
