import { describe, expect, it, vi } from "vitest";

import {
  advancesAttentionNotificationRevision,
  deliverPendingSurface,
  type SurfaceRecord,
} from "./attentionNotificationSurfaces";

describe("attention notification surfaces", () => {
  it("applies only the first or a newer revision for one notification ID", () => {
    expect(advancesAttentionNotificationRevision(undefined, 1)).toBe(true);
    expect(advancesAttentionNotificationRevision(1, 1)).toBe(false);
    expect(advancesAttentionNotificationRevision(2, 1)).toBe(false);
    expect(advancesAttentionNotificationRevision(1, 2)).toBe(true);
  });

  it("keeps failed native delivery pending instead of inventing dismissal", async () => {
    const notification = {
      id: { kind: "approval" as const, uuid: "approval-1" },
      kind: "approval" as const,
      occurredAt: "2026-08-13T00:00:00Z",
      revision: 1,
      question: null,
      approval: { message: "Approve?" },
      workflowApproval: null,
      interruptedCurrentNode: null,
      target: {
        kind: "workflow_task" as const,
        workflowID: "11111111-1111-4111-8111-111111111111",
        taskID: "task-1",
        focus: { kind: "approval" as const, approvalID: "approval-1" },
      },
    };
    const surfaced = new Map<string, SurfaceRecord>([
      ["k8_approvalu10_approval-1", { notification, state: "surfacing" }],
    ]);

    await deliverPendingSurface({
      focused: false,
      hasNativeNotifications: true,
      logger: { append: vi.fn(async () => undefined) },
      notification,
      notifications: {
        notify: vi.fn(async () => {
          throw new Error("native delivery failed");
        }),
        onActivated: vi.fn(async () => () => undefined),
        permissionState: vi.fn(async () => "granted" as const),
        removeActive: vi.fn(async () => undefined),
        requestPermission: vi.fn(async () => "granted" as const),
      },
      showToast: vi.fn(),
      surfaced,
      t: (key) => key,
    });

    expect(surfaced.get("k8_approvalu10_approval-1")?.state).toBe("native_delivery_failed");
  });
});
