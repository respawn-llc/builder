import { queryKeys } from "./queryKeys";

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const urgentID = "942495c2-5958-4959-8445-94046ad74fbd";
const smallID = "11111111-1111-4111-8111-111111111111";
const featureID = "22222222-2222-4222-8222-222222222222";

describe("label query keys", () => {
  it("keys catalogs, assignments, and board reads by canonical label filter identity", () => {
    expect(queryKeys.projectLabels("project-1")).toEqual(["project-labels", "project-1"]);
    expect(queryKeys.taskLabels("task-1")).toEqual(["task-labels", "task-1"]);
    expect(queryKeys.projectBoardsRoot("project-1")).toEqual(["board", "project-1"]);
    expect(queryKeys.projectBoardNodeCardsRoot("project-1")).toEqual(["board-node-cards", "project-1"]);
    expect(queryKeys.projectTaskListsRoot("project-1")).toEqual(["task-list", "project-1"]);
    expect(
      queryKeys.board("project-1", "11111111-1111-4111-8111-111111111111", {
        kind: "named",
        mode: "all",
        labelIDs: [priorityID],
      }),
    ).toEqual(["board", "project-1", "11111111-1111-4111-8111-111111111111", "named", "all", "included", priorityID, "excluded"]);
    const cardKey = queryKeys.boardNodeCards("project-1", "11111111-1111-4111-8111-111111111111", "node-1", {
      kind: "unlabeled",
    });
    expect(cardKey).toEqual(["board-node-cards", "project-1", "11111111-1111-4111-8111-111111111111", "unlabeled", "node-1"]);
    expect(cardKey.slice(0, -1)).toEqual(
      queryKeys.boardNodeCardsRoot("project-1", "11111111-1111-4111-8111-111111111111", {
        kind: "unlabeled",
      }),
    );
    expect(
      queryKeys.board("project-1", "11111111-1111-4111-8111-111111111111", {
        kind: "named",
        mode: "any",
        labelIDs: [priorityID, urgentID],
      }),
    ).toEqual(
      queryKeys.board("project-1", "11111111-1111-4111-8111-111111111111", {
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

    expect(queryKeys.board("project-1", "11111111-1111-4111-8111-111111111111", filter)).toEqual([
      "board",
      "project-1",
      "11111111-1111-4111-8111-111111111111",
      "named",
      "any",
      "included",
      urgentID,
      priorityID,
      "excluded",
      smallID,
    ]);
    expect(queryKeys.board("project-1", "11111111-1111-4111-8111-111111111111", filter)).toEqual(
      queryKeys.board("project-1", "11111111-1111-4111-8111-111111111111", reordered),
    );
    expect(queryKeys.board("project-1", "11111111-1111-4111-8111-111111111111", filter)).not.toEqual(
      queryKeys.board("project-1", "11111111-1111-4111-8111-111111111111", differentExcluded),
    );
    expect(queryKeys.board("project-1", "11111111-1111-4111-8111-111111111111", filter)).not.toEqual(
      queryKeys.board("project-1", "11111111-1111-4111-8111-111111111111", swappedPolarity),
    );
    expect(queryKeys.boardNodeCardsRoot("project-1", "11111111-1111-4111-8111-111111111111", filter)).not.toEqual(
      queryKeys.boardNodeCardsRoot("project-1", "11111111-1111-4111-8111-111111111111", differentExcluded),
    );
    expect(queryKeys.boardNodeCards("project-1", "11111111-1111-4111-8111-111111111111", "node-1", filter).slice(0, -1)).toEqual(
      queryKeys.boardNodeCardsRoot("project-1", "11111111-1111-4111-8111-111111111111", filter),
    );
  });
});
