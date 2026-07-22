import { createLabelFilterState, reduceLabelFilterState } from "./labelFilterState";

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const urgentID = "942495c2-5958-4959-8445-94046ad74fbd";

describe("label filter state", () => {
  it("starts unrestricted and selects the first named label in OR mode", () => {
    const initial = createLabelFilterState();

    expect(initial).toEqual({
      filter: { kind: "none" },
      namedMode: "any",
    });
    expect(reduceLabelFilterState(initial, { type: "named.toggle", labelID: priorityID })).toEqual({
      filter: { kind: "named", mode: "any", labelIDs: [priorityID] },
      namedMode: "any",
    });
  });

  it("keeps named label identity sorted while toggling selections", () => {
    const priority = reduceLabelFilterState(createLabelFilterState(), {
      type: "named.toggle",
      labelID: priorityID,
    });
    const both = reduceLabelFilterState(priority, {
      type: "named.toggle",
      labelID: urgentID,
    });

    expect(both.filter).toEqual({
      kind: "named",
      mode: "any",
      labelIDs: [urgentID, priorityID],
    });
    expect(reduceLabelFilterState(both, { type: "named.toggle", labelID: urgentID }).filter).toEqual({
      kind: "named",
      mode: "any",
      labelIDs: [priorityID],
    });
  });

  it("switches named filters between OR and AND", () => {
    const selected = reduceLabelFilterState(createLabelFilterState(), {
      type: "named.toggle",
      labelID: priorityID,
    });

    expect(reduceLabelFilterState(selected, { type: "named.mode", mode: "all" })).toEqual({
      filter: { kind: "named", mode: "all", labelIDs: [priorityID] },
      namedMode: "all",
    });
  });

  it("makes unlabeled exclusive and restores the remembered named mode", () => {
    const selected = reduceLabelFilterState(createLabelFilterState(), {
      type: "named.toggle",
      labelID: priorityID,
    });
    const all = reduceLabelFilterState(selected, { type: "named.mode", mode: "all" });
    const unlabeled = reduceLabelFilterState(all, { type: "unlabeled.toggle" });

    expect(unlabeled).toEqual({
      filter: { kind: "unlabeled" },
      namedMode: "all",
    });
    expect(reduceLabelFilterState(unlabeled, { type: "named.toggle", labelID: urgentID })).toEqual({
      filter: { kind: "named", mode: "all", labelIDs: [urgentID] },
      namedMode: "all",
    });
    expect(reduceLabelFilterState(unlabeled, { type: "unlabeled.toggle" })).toEqual({
      filter: { kind: "none" },
      namedMode: "all",
    });
  });

  it("clears every filter and restores OR mode", () => {
    const selected = reduceLabelFilterState(createLabelFilterState(), {
      type: "named.toggle",
      labelID: priorityID,
    });
    const all = reduceLabelFilterState(selected, { type: "named.mode", mode: "all" });

    expect(reduceLabelFilterState(all, { type: "clear" })).toEqual({
      filter: { kind: "none" },
      namedMode: "any",
    });
  });

  it("prunes deleted named labels without changing an unlabeled filter", () => {
    const first = reduceLabelFilterState(createLabelFilterState(), {
      type: "named.toggle",
      labelID: priorityID,
    });
    const both = reduceLabelFilterState(first, {
      type: "named.toggle",
      labelID: urgentID,
    });

    expect(reduceLabelFilterState(both, { type: "label.deleted", labelID: urgentID })).toEqual({
      filter: { kind: "named", mode: "any", labelIDs: [priorityID] },
      namedMode: "any",
    });
    expect(
      reduceLabelFilterState(
        { filter: { kind: "unlabeled" }, namedMode: "all" },
        { type: "label.deleted", labelID: priorityID },
      ),
    ).toEqual({
      filter: { kind: "unlabeled" },
      namedMode: "all",
    });
  });
});
