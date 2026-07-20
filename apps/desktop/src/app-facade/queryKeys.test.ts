import { queryKeys } from "./queryKeys";

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const urgentID = "942495c2-5958-4959-8445-94046ad74fbd";

describe("label query keys", () => {
  it("keys catalogs, assignments, and board reads by canonical label filter identity", () => {
    expect(queryKeys.projectLabels("project-1")).toEqual(["project-labels", "project-1"]);
    expect(queryKeys.taskLabels("task-1")).toEqual(["task-labels", "task-1"]);
    expect(
      queryKeys.board("project-1", "workflow-1", {
        kind: "named",
        mode: "all",
        labelIDs: [priorityID],
      }),
    ).toEqual(["board", "project-1", "workflow-1", "named", "all", priorityID]);
    const cardKey = queryKeys.boardNodeCards("project-1", "workflow-1", "node-1", {
      kind: "unlabeled",
    });
    expect(cardKey).toEqual(["board-node-cards", "project-1", "workflow-1", "unlabeled", "node-1"]);
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
});
