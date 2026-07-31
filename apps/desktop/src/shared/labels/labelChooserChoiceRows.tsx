import type { Dispatch, SetStateAction } from "react";

import type { VerticalReorderRow } from "@app/ui-kit";
import type { ProjectLabel } from "@/api";
import type { LabelChooserInvocation } from "./LabelChooser";
import {
  LabelRenameEditor,
  LabelResultRow,
  UnlabeledResultRow,
  type DeleteState,
  type LabelResultRowSelection,
  type RenameState,
} from "./LabelChooserRows";
import type { LabelFilterState } from "./labelFilterState";

export type LabelChooserChoice =
  | Readonly<{ kind: "unlabeled" }>
  | Readonly<{ kind: "label"; label: ProjectLabel }>;

export function renderLabelChooserChoiceRow({
  choice,
  confirmDelete,
  commitRename,
  deletion,
  highlighted,
  invocation,
  rename,
  reorderRow,
  setDeletion,
  setRename,
  unlabeledName,
}: Readonly<{
  choice: LabelChooserChoice;
  confirmDelete(): Promise<void>;
  commitRename(): Promise<void>;
  deletion: DeleteState | null;
  highlighted: boolean;
  invocation: LabelChooserInvocation;
  rename: RenameState | null;
  reorderRow?: VerticalReorderRow | undefined;
  setDeletion: Dispatch<SetStateAction<DeleteState | null>>;
  setRename: Dispatch<SetStateAction<RenameState | null>>;
  unlabeledName: string;
}>) {
  if (choice.kind === "unlabeled") {
    return (
      <UnlabeledResultRow
        highlighted={highlighted}
        key="unlabeled"
        name={unlabeledName}
        onSelect={() => {
          selectUnlabeled(invocation);
        }}
        selected={invocation.kind === "filter" && invocation.state.filter.kind === "unlabeled"}
      />
    );
  }
  const { label } = choice;
  if (rename?.labelID === label.id) {
    return (
      <LabelRenameEditor
        key={label.id}
        onCancel={() => {
          setRename(null);
        }}
        onChange={(draft) => {
          setRename({ ...rename, draft, error: null });
        }}
        onCommit={() => {
          void commitRename();
        }}
        rename={rename}
      />
    );
  }
  const selection = labelResultRowSelection(invocation, label.id);
  const labelDeletion = deletion?.labelID === label.id ? deletion : null;
  return (
    <LabelResultRow
      deletion={labelDeletion}
      highlighted={highlighted}
      key={label.id}
      label={label}
      onDeleteConfirm={() => {
        void confirmDelete();
      }}
      onDeleteOpenChange={(nextOpen) => {
        if (nextOpen) {
          setDeletion({
            labelID: label.id,
            error: null,
            pending: false,
          });
          return;
        }
        setDeletion((current) => (current?.labelID === label.id && current.pending ? current : null));
      }}
      onRename={() => {
        setRename({
          labelID: label.id,
          draft: label.name,
          error: null,
          pending: false,
        });
      }}
      onSelect={() => {
        selectLabel(invocation, label.id, selection.kind === "binary" ? !selection.selected : true);
      }}
      reorderRow={reorderRow}
      selection={selection}
    />
  );
}

export function labelResultRowSelection(
  invocation: LabelChooserInvocation,
  labelID: string,
): LabelResultRowSelection {
  if (invocation.kind === "assignment") {
    return {
      kind: "binary",
      selected: invocation.selectedLabelIDs.includes(labelID),
    };
  }
  return {
    kind: "condition",
    state: labelFilterCondition(invocation.state, labelID),
  };
}

function labelFilterCondition(state: LabelFilterState, labelID: string): "neutral" | "included" | "excluded" {
  if (state.filter.kind !== "named") {
    return "neutral";
  }
  if (state.filter.labelIDs.includes(labelID)) {
    return "included";
  }
  return state.filter.excludedLabelIDs.includes(labelID) ? "excluded" : "neutral";
}

export function selectLabel(invocation: LabelChooserInvocation, labelID: string, selected: boolean): void {
  if (invocation.kind === "filter") {
    invocation.onAction({ type: "named.cycle", labelID });
    return;
  }
  invocation.onSelectionChange(labelID, selected);
}

export function selectUnlabeled(invocation: LabelChooserInvocation): void {
  if (invocation.kind === "filter") {
    invocation.onAction({ type: "unlabeled.toggle" });
  }
}

export function removeDeletedSelection(invocation: LabelChooserInvocation, labelID: string): void {
  if (invocation.kind === "filter") {
    invocation.onAction({ type: "label.deleted", labelID });
    return;
  }
  if (invocation.selectedLabelIDs.includes(labelID)) {
    invocation.onSelectionChange(labelID, false);
  }
}
