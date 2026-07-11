import { useState } from "react";
import { useTranslation } from "react-i18next";

import type {
  WorkflowTaskExecutionTargetSelection,
  WorkflowTaskExecutionTargetSelectionMode,
  WorkflowTaskExecutionTargetSelectionRequired,
} from "../../api";
import { Button, Dialog, SelectField, TextInput } from "../../ui";

export function ExecutionTargetSelectionDialog({
  onClose,
  onSubmit,
  requirement,
}: Readonly<{
  onClose: () => void;
  onSubmit: (selection: WorkflowTaskExecutionTargetSelection) => void;
  requirement: WorkflowTaskExecutionTargetSelectionRequired;
}>) {
  const { t } = useTranslation();
  const [selectionState, setSelectionState] = useState(() => selectionDialogState(requirement));
  const state =
    selectionState.generation === requirement.generation
      ? selectionState
      : selectionDialogState(requirement);
  if (state !== selectionState) {
    setSelectionState(state);
  }
  const selection = state.selection;
  const customRefRequired = selection === "custom_ref";
  const customRefValid = !customRefRequired || state.customRef.trim().length > 0;

  function submit(): void {
    if (selection === "custom_ref") {
      if (!customRefValid) {
        return;
      }
      onSubmit({ customRef: state.customRef, mode: selection });
      return;
    }
    onSubmit({ customRef: null, mode: selection });
  }

  return (
    <Dialog
      closeLabel={t("app.close")}
      onClose={onClose}
      open
      title={t("task.executionTargetDialogTitle")}
    >
      <form
        className="grid gap-[var(--space-4)]"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <p className="m-0 text-[var(--color-muted)]">{t("task.executionTargetDialogBody")}</p>
        <SelectField
          label={t("task.executionTarget")}
          onValueChange={(value) => {
            setSelectionState({
              ...state,
              selection: selectionModeFromValue(value, requirement.supportedSelections),
            });
          }}
          options={requirement.supportedSelections.map((mode) => ({
            label: selectionLabel(mode, t),
            value: mode,
          }))}
          value={selection}
        />
        {customRefRequired ? (
          <TextInput
            autoFocus
            label={t("task.executionTargetCustomRef")}
            onChange={(event) => {
              setSelectionState({ ...state, customRef: event.target.value });
            }}
            value={state.customRef}
          />
        ) : null}
        <div className="flex justify-end gap-[var(--space-2)]">
          <Button onClick={onClose}>{t("app.cancel")}</Button>
          <Button disabled={!customRefValid} type="submit" variant="primary">
            {t("task.executionTargetContinue")}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

type SelectionDialogState = Readonly<{
  generation: string;
  selection: WorkflowTaskExecutionTargetSelectionMode;
  customRef: string;
}>;

function selectionDialogState(
  requirement: WorkflowTaskExecutionTargetSelectionRequired,
): SelectionDialogState {
  const selection = requirement.supportedSelections[0];
  if (selection === undefined) {
    throw new Error("execution target selection requirement has no supported selections");
  }
  return {
    customRef: "",
    generation: requirement.generation,
    selection,
  };
}

function selectionModeFromValue(
  value: string,
  supportedSelections: readonly WorkflowTaskExecutionTargetSelectionMode[],
): WorkflowTaskExecutionTargetSelectionMode {
  const selection = supportedSelections.find((mode) => mode === value);
  if (selection === undefined) {
    throw new Error(`unsupported execution target selection ${value}`);
  }
  return selection;
}

function selectionLabel(
  selection: WorkflowTaskExecutionTargetSelectionMode,
  t: (key: string) => string,
): string {
  switch (selection) {
    case "none":
      return t("task.executionTargetNone");
    case "head":
      return t("task.executionTargetHead");
    case "default_branch":
      return t("task.executionTargetDefaultBranch");
    case "custom_ref":
      return t("task.executionTargetCustomRef");
  }
}
