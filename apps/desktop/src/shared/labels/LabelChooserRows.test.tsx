import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect, useState } from "react";

import { createLabelFilterState, reduceLabelFilterState, type LabelFilterState } from "./labelFilterState";
import { LabelResultRow, UnlabeledResultRow, type LabelFilterCondition } from "./LabelChooserRows";

const priorityID = "f74ce532-9e6e-4cf6-b3c1-d67d5a3eedcf";
const label = { id: priorityID, name: "Priority" };

describe("Label chooser rows", () => {
  it("cycles a newly available named filter row through typed conditions by pointer and keyboard", async () => {
    const user = userEvent.setup();
    const states: LabelFilterState[] = [];
    render(
      <NamedFilterRowHarness
        onState={(state) => {
          states.push(state);
        }}
      />,
    );

    const row = screen.getAllByRole("button").at(0);
    if (row === undefined) {
      throw new Error("Named label row did not render a selectable button.");
    }
    expect(row).not.toHaveAttribute("aria-pressed");
    expect(row).toHaveAttribute("aria-describedby");

    await user.click(row);
    await waitFor(() => {
      expect(states.at(-1)?.filter).toEqual({
        kind: "named",
        mode: "any",
        labelIDs: [priorityID],
        excludedLabelIDs: [],
      });
    });
    row.focus();
    await user.keyboard("{Enter}");
    await waitFor(() => {
      expect(states.at(-1)?.filter).toEqual({
        kind: "named",
        mode: "any",
        labelIDs: [],
        excludedLabelIDs: [priorityID],
      });
    });
    await user.keyboard("{Enter}");
    await waitFor(() => {
      expect(states.at(-1)).toEqual(createLabelFilterState());
    });
  });

  it("retains binary pressed semantics for assignment and No labels rows", () => {
    render(
      <>
        <LabelResultRow
          deletion={null}
          highlighted={false}
          label={{ id: "942495c2-5958-4959-8445-94046ad74fbd", name: "Assignment" }}
          onDeleteConfirm={noOp}
          onDeleteOpenChange={noOp}
          onRename={noOp}
          onSelect={noOp}
          selection={{ kind: "binary", selected: true }}
        />
        <UnlabeledResultRow highlighted={false} name="No labels" onSelect={noOp} selected />
      </>,
    );

    expect(screen.getByRole("button", { name: "Assignment" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "No labels" })).toHaveAttribute("aria-pressed", "true");
  });
});

function NamedFilterRowHarness({ onState }: Readonly<{ onState(state: LabelFilterState): void }>) {
  const [state, setState] = useState(createLabelFilterState);
  useEffect(() => {
    onState(state);
  }, [onState, state]);
  const condition = filterCondition(state);
  return (
    <LabelResultRow
      deletion={null}
      highlighted={false}
      label={label}
      onDeleteConfirm={noOp}
      onDeleteOpenChange={noOp}
      onRename={noOp}
      onSelect={() => {
        setState((current) => reduceLabelFilterState(current, { type: "named.cycle", labelID: priorityID }));
      }}
      selection={{ kind: "condition", state: condition }}
    />
  );
}

function noOp(): void {
  // The isolated row test does not exercise contextual mutation handlers.
}

function filterCondition(state: LabelFilterState): LabelFilterCondition {
  if (state.filter.kind !== "named") {
    return "neutral";
  }
  if (state.filter.labelIDs.includes(priorityID)) {
    return "included";
  }
  return state.filter.excludedLabelIDs.includes(priorityID) ? "excluded" : "neutral";
}
