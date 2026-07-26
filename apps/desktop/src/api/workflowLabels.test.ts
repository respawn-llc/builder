import { describe, expect, it } from "vitest";

import { taskLabelFilterConditionCount, taskLabelFiltersEqual } from "./workflowLabels";

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const urgentID = "942495c2-5958-4959-8445-94046ad74fbd";
const smallID = "11111111-1111-4111-8111-111111111111";

describe("Task label filters", () => {
  it("counts every included and excluded named condition", () => {
    expect(
      taskLabelFilterConditionCount({
        kind: "named",
        mode: "all",
        labelIDs: [priorityID, urgentID],
        excludedLabelIDs: [smallID],
      }),
    ).toBe(3);
    expect(taskLabelFilterConditionCount({ kind: "none" })).toBe(0);
    expect(taskLabelFilterConditionCount({ kind: "unlabeled" })).toBe(0);
  });

  it("compares canonical label filters across both partitions", () => {
    const filter = {
      kind: "named" as const,
      mode: "any" as const,
      labelIDs: [priorityID],
      excludedLabelIDs: [urgentID],
    };

    expect(
      taskLabelFiltersEqual(filter, {
        ...filter,
        labelIDs: [priorityID],
        excludedLabelIDs: [urgentID],
      }),
    ).toBe(true);
    expect(
      taskLabelFiltersEqual(filter, {
        ...filter,
        excludedLabelIDs: [smallID],
      }),
    ).toBe(false);
  });
});
