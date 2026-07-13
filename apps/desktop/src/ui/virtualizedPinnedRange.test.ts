import { defaultRangeExtractor } from "@tanstack/react-virtual";
import { describe, expect, it } from "vitest";

import { pinnedVirtualRangeExtractor } from "./virtualizedPinnedRange";

describe("pinnedVirtualRangeExtractor", () => {
  it("adds pinned indexes beyond overscan without duplicating the ordinary range", () => {
    const range = {
      startIndex: 10,
      endIndex: 15,
      overscan: 2,
      count: 100,
    };

    expect(pinnedVirtualRangeExtractor(range, new Set([12, 99]))).toEqual([
      ...defaultRangeExtractor(range),
      99,
    ]);
  });

  it("returns the ordinary virtual range after the pin clears", () => {
    const range = {
      startIndex: 10,
      endIndex: 15,
      overscan: 2,
      count: 100,
    };

    expect(pinnedVirtualRangeExtractor(range, new Set())).toEqual(defaultRangeExtractor(range));
  });
});
