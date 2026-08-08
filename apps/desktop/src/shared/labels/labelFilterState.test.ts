import {
  createLabelFilterState,
  reconcileLabelFilterState,
  reduceLabelFilterState,
} from "./labelFilterState";

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const urgentID = "942495c2-5958-4959-8445-94046ad74fbd";

describe("label filter state", () => {
  it("cycles a named condition from neutral to included to excluded to neutral", () => {
    const initial = createLabelFilterState();

    expect(initial).toEqual({
      filter: { kind: "none" },
      namedMode: "any",
    });
    const included = reduceLabelFilterState(initial, { type: "named.cycle", labelID: priorityID });
    expect(included).toEqual({
      filter: {
        kind: "named",
        mode: "any",
        labelIDs: [priorityID],
        excludedLabelIDs: [],
      },
      namedMode: "any",
    });
    const excluded = reduceLabelFilterState(included, { type: "named.cycle", labelID: priorityID });
    expect(excluded).toEqual({
      filter: {
        kind: "named",
        mode: "any",
        labelIDs: [],
        excludedLabelIDs: [priorityID],
      },
      namedMode: "any",
    });
    expect(reduceLabelFilterState(excluded, { type: "named.cycle", labelID: priorityID })).toEqual(
      createLabelFilterState(),
    );
  });

  it("keeps both named partitions sorted and disjoint", () => {
    const priorityIncluded = reduceLabelFilterState(createLabelFilterState(), {
      type: "named.cycle",
      labelID: priorityID,
    });
    const bothIncluded = reduceLabelFilterState(priorityIncluded, {
      type: "named.cycle",
      labelID: urgentID,
    });
    const priorityExcluded = reduceLabelFilterState(bothIncluded, {
      type: "named.cycle",
      labelID: priorityID,
    });
    const bothExcluded = reduceLabelFilterState(priorityExcluded, {
      type: "named.cycle",
      labelID: urgentID,
    });

    expect(bothIncluded.filter).toEqual({
      kind: "named",
      mode: "any",
      labelIDs: [urgentID, priorityID],
      excludedLabelIDs: [],
    });
    expect(priorityExcluded.filter).toEqual({
      kind: "named",
      mode: "any",
      labelIDs: [urgentID],
      excludedLabelIDs: [priorityID],
    });
    expect(bothExcluded.filter).toEqual({
      kind: "named",
      mode: "any",
      labelIDs: [],
      excludedLabelIDs: [urgentID, priorityID],
    });
  });

  it("switches named filters between OR and AND", () => {
    const selected = reduceLabelFilterState(createLabelFilterState(), {
      type: "named.cycle",
      labelID: priorityID,
    });

    expect(reduceLabelFilterState(selected, { type: "named.mode", mode: "all" })).toEqual({
      filter: { kind: "named", mode: "all", labelIDs: [priorityID], excludedLabelIDs: [] },
      namedMode: "all",
    });
  });

  it("makes unlabeled exclusive and restores the remembered named mode", () => {
    const included = reduceLabelFilterState(createLabelFilterState(), {
      type: "named.cycle",
      labelID: priorityID,
    });
    const excluded = reduceLabelFilterState(included, { type: "named.cycle", labelID: priorityID });
    const all = reduceLabelFilterState(excluded, { type: "named.mode", mode: "all" });
    const unlabeled = reduceLabelFilterState(all, { type: "unlabeled.toggle" });

    expect(unlabeled).toEqual({
      filter: { kind: "unlabeled" },
      namedMode: "all",
    });
    expect(reduceLabelFilterState(unlabeled, { type: "named.cycle", labelID: urgentID })).toEqual({
      filter: { kind: "named", mode: "all", labelIDs: [urgentID], excludedLabelIDs: [] },
      namedMode: "all",
    });
    expect(reduceLabelFilterState(unlabeled, { type: "unlabeled.toggle" })).toEqual({
      filter: { kind: "none" },
      namedMode: "all",
    });
  });

  it("clears every filter and restores OR mode", () => {
    const selected = reduceLabelFilterState(createLabelFilterState(), {
      type: "named.cycle",
      labelID: priorityID,
    });
    const all = reduceLabelFilterState(selected, { type: "named.mode", mode: "all" });

    expect(reduceLabelFilterState(all, { type: "clear" })).toEqual({
      filter: { kind: "none" },
      namedMode: "any",
    });
  });

  it("prunes and reconciles both named partitions without changing an unlabeled filter", () => {
    const named = {
      filter: {
        kind: "named" as const,
        mode: "all" as const,
        labelIDs: [priorityID],
        excludedLabelIDs: [urgentID],
      },
      namedMode: "all" as const,
    };

    expect(reduceLabelFilterState(named, { type: "label.deleted", labelID: urgentID })).toEqual({
      filter: { kind: "named", mode: "all", labelIDs: [priorityID], excludedLabelIDs: [] },
      namedMode: "all",
    });
    expect(reduceLabelFilterState(named, { type: "label.deleted", labelID: priorityID })).toEqual({
      filter: { kind: "named", mode: "all", labelIDs: [], excludedLabelIDs: [urgentID] },
      namedMode: "all",
    });
    expect(reconcileLabelFilterState(named, [urgentID])).toEqual({
      filter: { kind: "named", mode: "all", labelIDs: [], excludedLabelIDs: [urgentID] },
      namedMode: "all",
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
