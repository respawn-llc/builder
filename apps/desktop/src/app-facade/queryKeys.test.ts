import type { BoardNodeCardsSort } from "@/api";
import { queryKeys } from "./queryKeys";

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const urgentID = "942495c2-5958-4959-8445-94046ad74fbd";
const smallID = "11111111-1111-4111-8111-111111111111";
const featureID = "22222222-2222-4222-8222-222222222222";

describe("label query keys", () => {
  it("keys board and card reads by canonical sort identity", () => {
    const filter = { kind: "none" as const };
    const ascendingTitle: BoardNodeCardsSort = { field: "title", direction: "asc" };
    const descendingTitle: BoardNodeCardsSort = { field: "title", direction: "desc" };

    expect(queryKeys.board("project-1", "workflow-1", filter, ascendingTitle)).not.toEqual(
      queryKeys.board("project-1", "workflow-1", filter, descendingTitle),
    );
    expect(queryKeys.boardNodeCardsRoot("project-1", "workflow-1", filter, ascendingTitle)).not.toEqual(
      queryKeys.boardNodeCardsRoot("project-1", "workflow-1", filter, descendingTitle),
    );
    expect(queryKeys.boardNodeCards("project-1", "workflow-1", "node-1", {
      labelFilter: filter,
      sort: ascendingTitle,
    })).toEqual([
      "board-node-cards",
      "project-1",
      "workflow-1",
      "none",
      "sort",
      "title",
      "asc",
      "node-1",
    ]);
  });

  it("keys catalogs, assignments, and board reads by canonical label filter identity", () => {
    expect(queryKeys.projectLabels("project-1")).toEqual(["project-labels", "project-1"]);
    expect(queryKeys.taskLabels("task-1")).toEqual(["task-labels", "task-1"]);
    expect(queryKeys.projectBoardsRoot("project-1")).toEqual(["board", "project-1"]);
    expect(queryKeys.projectBoardNodeCardsRoot("project-1")).toEqual(["board-node-cards", "project-1"]);
    expect(queryKeys.projectTaskListsRoot("project-1")).toEqual(["task-list", "project-1"]);
    expect(
      queryKeys.board("project-1", "workflow-1", {
        kind: "named",
        mode: "all",
        labelIDs: [priorityID],
      }),
    ).toEqual([
      "board",
      "project-1",
      "workflow-1",
      "named",
      "all",
      "included",
      priorityID,
      "excluded",
      "sort",
      "updated",
      "desc",
    ]);
    const cardKey = queryKeys.boardNodeCards("project-1", "workflow-1", "node-1", {
      labelFilter: { kind: "unlabeled" },
    });
    expect(cardKey).toEqual([
      "board-node-cards",
      "project-1",
      "workflow-1",
      "unlabeled",
      "sort",
      "updated",
      "desc",
      "node-1",
    ]);
    expect(cardKey.slice(0, -1)).toEqual(
      queryKeys.boardNodeCardsRoot("project-1", "workflow-1", {
        kind: "unlabeled",
      }),
    );
    expect(
      queryKeys.board("project-1", "workflow-1", {
        kind: "named",
        mode: "any",
        labelIDs: [priorityID, urgentID],
      }),
    ).toEqual(
      queryKeys.board("project-1", "workflow-1", {
        kind: "named",
        mode: "any",
        labelIDs: [urgentID, priorityID],
      }),
    );
  });

  it("keeps included and excluded partitions distinct for board and card identities", () => {
    const filter = {
      kind: "named" as const,
      mode: "any" as const,
      labelIDs: [priorityID, urgentID],
      excludedLabelIDs: [smallID],
    };
    const reordered = {
      ...filter,
      labelIDs: [urgentID, priorityID],
    };
    const differentExcluded = {
      ...filter,
      excludedLabelIDs: [featureID],
    };
    const swappedPolarity = {
      ...filter,
      labelIDs: [smallID],
      excludedLabelIDs: [urgentID, priorityID],
    };

    expect(queryKeys.board("project-1", "workflow-1", filter)).toEqual([
      "board",
      "project-1",
      "workflow-1",
      "named",
      "any",
      "included",
      urgentID,
      priorityID,
      "excluded",
      smallID,
      "sort",
      "updated",
      "desc",
    ]);
    expect(queryKeys.board("project-1", "workflow-1", filter)).toEqual(
      queryKeys.board("project-1", "workflow-1", reordered),
    );
    expect(queryKeys.board("project-1", "workflow-1", filter)).not.toEqual(
      queryKeys.board("project-1", "workflow-1", differentExcluded),
    );
    expect(queryKeys.board("project-1", "workflow-1", filter)).not.toEqual(
      queryKeys.board("project-1", "workflow-1", swappedPolarity),
    );
    expect(queryKeys.boardNodeCardsRoot("project-1", "workflow-1", filter)).not.toEqual(
      queryKeys.boardNodeCardsRoot("project-1", "workflow-1", differentExcluded),
    );
    expect(
      queryKeys.boardNodeCards("project-1", "workflow-1", "node-1", { labelFilter: filter }).slice(0, -1),
    ).toEqual(
      queryKeys.boardNodeCardsRoot("project-1", "workflow-1", filter),
    );
  });
});
