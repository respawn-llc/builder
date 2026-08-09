import { describe, expect, it } from "vitest";

import { registeredWorktreeTopologySchema } from "./worktreeTopology";

const registeredWorktree = {
  variant: "registered",
  registered: {
    git: {
      canonical_root: "/repo/feature",
      head_object: "0123456789abcdef",
      branch_ref: null,
      branch_name: null,
      detached: true,
      bare: false,
      locked_reason: null,
      prunable_reason: null,
      is_main: false,
      path_available: true,
    },
    kent: {
      worktree_id: "worktree-1",
      canonical_root: "/repo/feature",
      display_name: "feature",
      managed: true,
      created_branch: false,
      origin_session_id: null,
    },
  },
} as const;

describe("registeredWorktreeTopologySchema", () => {
  it("requires explicit null for absent optional facts", () => {
    expect(() =>
      registeredWorktreeTopologySchema.parse({
        ...registeredWorktree,
        registered: {
          ...registeredWorktree.registered,
          git: {
            canonical_root: registeredWorktree.registered.git.canonical_root,
            head_object: registeredWorktree.registered.git.head_object,
            branch_name: null,
            detached: true,
            bare: false,
            locked_reason: null,
            prunable_reason: null,
            is_main: false,
            path_available: true,
          },
        },
      }),
    ).toThrow();
    expect(registeredWorktreeTopologySchema.parse(registeredWorktree)).toMatchObject({
      registered: {
        git: { branchRef: null },
        kent: { originSessionID: null },
      },
    });
  });
});
