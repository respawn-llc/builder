import { describe, expect, it } from "vitest";

import {
  boardFiltersEqual,
  boardFilterWithDependencyFilter,
  boardFilterWithLabelFilter,
  canonicalBoardFilter,
  type BoardFilter,
} from "./workflowBoardFilters";

const labelFilter = {
  kind: "named" as const,
  mode: "all" as const,
  labelIDs: ["b", "a"],
};

describe("workflow board filters", () => {
  it("canonicalizes and compares both filter members", () => {
    const filter = canonicalBoardFilter({ labelFilter, dependencyFilter: null });
    expect(filter).toEqual({
      labelFilter: { kind: "named", mode: "all", labelIDs: ["a", "b"], excludedLabelIDs: [] },
      dependencyFilter: null,
    });
    expect(
      boardFiltersEqual(
        filter,
        canonicalBoardFilter({
          labelFilter: { kind: "named", mode: "all", labelIDs: ["a", "b"] },
          dependencyFilter: null,
        }),
      ),
    ).toBe(true);
    expect(boardFiltersEqual(filter, { ...filter, dependencyFilter: true })).toBe(false);
  });

  it("updates one member without changing the other", () => {
    const source: BoardFilter = canonicalBoardFilter({ labelFilter, dependencyFilter: true });
    expect(boardFilterWithLabelFilter(source, { kind: "unlabeled" })).toEqual({
      labelFilter: { kind: "unlabeled" },
      dependencyFilter: true,
    });
    expect(boardFilterWithDependencyFilter(source, null)).toEqual({
      ...source,
      dependencyFilter: null,
    });
  });
});
