import { canonicalBoardFilter } from "@/api";
import { queryKeys } from "./queryKeys";

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const urgentID = "942495c2-5958-4959-8445-94046ad74fbd";
const smallID = "11111111-1111-4111-8111-111111111111";
const featureID = "22222222-2222-4222-8222-222222222222";

describe("board query identities", () => {
  it("canonicalizes equivalent Label filters and keeps board/card scopes distinct", () => {
    const namedLabelFilter = {
      kind: "named" as const,
      mode: "any" as const,
      labelIDs: [priorityID, urgentID],
      excludedLabelIDs: [smallID],
    };
    const filter = canonicalBoardFilter({
      labelFilter: namedLabelFilter,
      dependencyFilter: null,
    });
    const reordered = canonicalBoardFilter({
      labelFilter: {
        kind: "named",
        mode: "any",
        labelIDs: [urgentID, priorityID],
        excludedLabelIDs: [smallID],
      },
      dependencyFilter: null,
    });

    expect(queryKeys.board("project-1", "workflow-1", filter)).toEqual(
      queryKeys.board("project-1", "workflow-1", reordered),
    );
    expect(queryKeys.board("project-1", "workflow-1", filter)).not.toEqual(
      queryKeys.board(
        "project-1",
        "workflow-1",
        canonicalBoardFilter({
          labelFilter: { ...namedLabelFilter, excludedLabelIDs: [featureID] },
          dependencyFilter: null,
        }),
      ),
    );
    expect(
      queryKeys.boardNodeCards({
        filter,
        nodeID: "node-1",
        projectID: "project-1",
        workflowID: "workflow-1",
      }),
    ).not.toEqual(
      queryKeys.boardNodeCards({
        filter,
        nodeID: "node-2",
        projectID: "project-1",
        workflowID: "workflow-1",
      }),
    );
    expect(
      queryKeys.boardNodeCards({
        filter,
        nodeID: "node-1",
        projectID: "project-1",
        workflowID: "workflow-1",
      }),
    ).not.toEqual(
      queryKeys.boardNodeCards({
        filter,
        nodeID: "node-1",
        projectID: "project-1",
        sort: { field: "created", direction: "asc" },
        workflowID: "workflow-1",
      }),
    );
  });

  it("distinguishes all dependency-filter values for the same Label filter", () => {
    const labelFilter = { kind: "none" as const };
    const all = queryKeys.board(
      "project-1",
      "workflow-1",
      canonicalBoardFilter({ labelFilter, dependencyFilter: null }),
    );
    const unblocked = queryKeys.board(
      "project-1",
      "workflow-1",
      canonicalBoardFilter({ labelFilter, dependencyFilter: true }),
    );
    const blocked = queryKeys.board(
      "project-1",
      "workflow-1",
      canonicalBoardFilter({ labelFilter, dependencyFilter: false }),
    );

    expect(all).not.toEqual(unblocked);
    expect(all).not.toEqual(blocked);
    expect(unblocked).not.toEqual(blocked);
  });

  it("keeps Project Task counts and independent groups below one invalidation root", () => {
    const root = queryKeys.projectTaskListsRoot("project-1");
    const counts = queryKeys.projectTaskGroupCounts("project-1");
    const active = queryKeys.projectTaskGroup("project-1", "active");
    const backlog = queryKeys.projectTaskGroup("project-1", "backlog");

    expect(counts.slice(0, root.length)).toEqual(root);
    expect(active.slice(0, root.length)).toEqual(root);
    expect(active).not.toEqual(backlog);
  });
});
