import { describe, expect, it } from "vitest";

import { advancesAttentionNotificationRevision } from "./attentionNotificationSurfaces";

describe("attention notification surfaces", () => {
  it("applies only the first or a newer revision for one notification ID", () => {
    expect(advancesAttentionNotificationRevision(undefined, 1)).toBe(true);
    expect(advancesAttentionNotificationRevision(1, 1)).toBe(false);
    expect(advancesAttentionNotificationRevision(2, 1)).toBe(false);
    expect(advancesAttentionNotificationRevision(1, 2)).toBe(true);
  });
});
