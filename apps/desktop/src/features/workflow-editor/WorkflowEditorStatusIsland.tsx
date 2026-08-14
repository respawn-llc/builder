import { useState } from "react";
import { useTranslation } from "react-i18next";

import type { WorkflowGraphSaveImpact, WorkflowGraphSavePreview, WorkflowValidationError } from "@/api";
import { normalizeWorkflowValidationErrors, WorkflowValidationIssues } from "@/shared/workflow-validation";
import { Button, FloatingNoticeIsland } from "@/ui";
import type { WorkflowEditorDraftController } from "./workflowEditorDraftBridgeCore";

export function WorkflowEditorStatusIsland({
  confirmationPreview,
  controller,
  onCancelConfirmation,
  onConfirmSave,
  onDiscard,
  positionStrategy,
}: Readonly<{
  confirmationPreview: WorkflowGraphSavePreview | null;
  controller: WorkflowEditorDraftController;
  onCancelConfirmation: () => void;
  onConfirmSave: () => void;
  onDiscard: () => void;
  positionStrategy: "absolute" | "fixed";
}>) {
  const { t } = useTranslation();
  const [collapsed, setCollapsed] = useState(false);
  const validationErrors = normalizeWorkflowValidationErrors(statusIslandValidationErrors(controller));
  const hasIssues =
    validationErrors.length > 0 || controller.saveBlockers.length > 0 || controller.saveError.length > 0;
  if (!controller.dirty.dirty && controller.state.conflict === null && !hasIssues) {
    return null;
  }
  return (
    <FloatingNoticeIsland
      collapsed={collapsed}
      collapseLabel={t("app.collapse")}
      expandedClassName="floating-notice-expanded grid max-h-[min(400px,calc(100vh-32px))] w-[min(400px,calc(100vw-32px))] gap-[6px] rounded-[var(--radius-xl)] p-[var(--space-3)]"
      expandLabel={t("app.expand")}
      level={3}
      onCollapsedChange={setCollapsed}
      positionClassName="right-[var(--space-4)] bottom-[var(--space-4)]"
      positionStrategy={positionStrategy}
      title={controller.dirty.dirty ? t("workflowEditor.unsavedChanges") : t("board.workflowIssues")}
      tone={statusIslandTone(controller)}
    >
      <div className="grid gap-[var(--space-3)] pt-[6px]">
        <WorkflowEditorSaveActions
          confirmationPreview={confirmationPreview}
          controller={controller}
          onCancelConfirmation={onCancelConfirmation}
          onConfirmSave={onConfirmSave}
          onDiscard={onDiscard}
        />
        {controller.state.conflict !== null ? <ConflictActions controller={controller} /> : null}
        {controller.saveError.length > 0 ? (
          <p className="m-0 text-sm text-[var(--color-error)]">{controller.saveError}</p>
        ) : null}
        {controller.saveBlockers.length > 0 ? (
          <ul className="m-0 grid gap-[var(--space-1)] p-0">
            {controller.saveBlockers.map((blocker) => (
              <li className="list-none text-sm text-[var(--color-error)]" key={blocker}>
                {blocker}
              </li>
            ))}
          </ul>
        ) : null}
        {validationErrors.length > 0 ? <WorkflowValidationIssues errors={validationErrors} /> : null}
      </div>
    </FloatingNoticeIsland>
  );
}

function statusIslandValidationErrors(
  controller: WorkflowEditorDraftController,
): readonly WorkflowValidationError[] {
  if (controller.dirty.graphDirty && controller.draftValidation === null) {
    // Background validation is disabled while the graph is dirty; fall back to
    // the structured result captured at the last blocked save attempt.
    return [
      ...errorsOrEmpty(controller.saveValidation?.draft),
      ...errorsOrEmpty(controller.saveValidation?.execution),
    ];
  }
  return [...errorsOrEmpty(controller.draftValidation), ...errorsOrEmpty(controller.executionValidation)];
}

function statusIslandTone(controller: WorkflowEditorDraftController): "danger" | "neutral" {
  return controller.draftValidation?.valid === false || controller.saveError.length > 0
    ? "danger"
    : "neutral";
}

function errorsOrEmpty(
  validation: { errors: readonly WorkflowValidationError[] } | null | undefined,
): readonly WorkflowValidationError[] {
  return validation?.errors ?? [];
}

export function WorkflowEditorSaveActions({
  confirmationPreview,
  controller,
  onCancelConfirmation,
  onConfirmSave,
  onDiscard,
}: Readonly<{
  confirmationPreview: WorkflowGraphSavePreview | null;
  controller: WorkflowEditorDraftController;
  onCancelConfirmation: () => void;
  onConfirmSave: () => void;
  onDiscard: () => void;
}>) {
  const { t } = useTranslation();
  if (confirmationPreview !== null) {
    return (
      <WorkflowSaveConfirmation
        disabled={controller.saving}
        impact={confirmationPreview.impact}
        onCancel={onCancelConfirmation}
        onConfirm={onConfirmSave}
      />
    );
  }
  if (!controller.dirty.dirty) {
    return null;
  }
  return (
    <div className="grid grid-cols-2 gap-[var(--space-2)]">
      <Button className="w-full" disabled={controller.saving} onClick={onDiscard} variant="danger">
        {t("workflowEditor.discard")}
      </Button>
      <Button
        className="w-full"
        disabled={
          controller.saving || (controller.dirty.graphDirty && controller.draftValidation?.valid === false)
        }
        onClick={controller.save}
        variant="primary"
      >
        {controller.saving ? t("workflowEditor.saving") : t("workflowEditor.save")}
      </Button>
    </div>
  );
}

function ConflictActions({ controller }: Readonly<{ controller: WorkflowEditorDraftController }>) {
  const { t } = useTranslation();
  return (
    <div className="grid gap-[var(--space-2)]">
      <p className="m-0 text-sm text-[var(--color-on-island)]">{t("workflowEditor.remoteConflict")}</p>
      <div className="flex flex-wrap gap-[var(--space-2)]">
        <Button
          onClick={() => {
            controller.dispatch({ type: "reloadConflict" });
          }}
          variant="secondary"
        >
          {t("workflowEditor.reloadRemote")}
        </Button>
        <Button
          onClick={() => {
            controller.dispatch({ type: "keepEditing" });
          }}
          variant="ghost"
        >
          {t("workflowEditor.keepEditing")}
        </Button>
      </div>
    </div>
  );
}

function WorkflowSaveConfirmation({
  disabled,
  impact,
  onCancel,
  onConfirm,
}: Readonly<{
  disabled: boolean;
  impact: WorkflowGraphSaveImpact;
  onCancel: () => void;
  onConfirm: () => void;
}>) {
  const { t } = useTranslation();
  return (
    <div className="grid gap-[var(--space-3)]">
      <p className="m-0 text-sm text-[var(--color-on-island)]">
        {t("workflowEditor.destructiveSaveWarning")}
      </p>
      <ul className="m-0 grid gap-[var(--space-1)] p-0 text-sm text-[var(--color-muted)]">
        <li className="list-none">
          {t("workflowEditor.saveImpactNodes", { count: impact.removedNodeCount })}
        </li>
        <li className="list-none">
          {t("workflowEditor.saveImpactTransitionGroups", {
            count: impact.removedTransitionGroupCount,
          })}
        </li>
        <li className="list-none">
          {t("workflowEditor.saveImpactEdges", { count: impact.removedEdgeCount })}
        </li>
      </ul>
      <div className="grid grid-cols-2 gap-[var(--space-2)]">
        <Button className="w-full" disabled={disabled} onClick={onCancel} variant="secondary">
          {t("app.cancel")}
        </Button>
        <Button className="w-full" disabled={disabled} onClick={onConfirm} variant="danger">
          {disabled ? t("workflowEditor.saving") : t("workflowEditor.confirmSave")}
        </Button>
      </div>
    </div>
  );
}
